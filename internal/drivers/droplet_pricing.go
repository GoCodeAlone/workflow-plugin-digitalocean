package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// DropletSizesClient matches the read-only godo Sizes API.
type DropletSizesClient interface {
	List(context.Context, *godo.ListOptions) ([]godo.Size, *godo.Response, error)
}

// DropletPriceQuote is diagnostic metadata, never authority for a later apply.
type DropletPriceQuote struct {
	SizeSlug      string    `json:"size_slug"`
	Region        string    `json:"region"`
	MonthlyUSD    float64   `json:"monthly_usd"`
	MaxMonthlyUSD float64   `json:"max_monthly_usd"`
	Source        string    `json:"source"`
	CheckedAt     time.Time `json:"checked_at"`
}

// QuoteDropletPrice fetches a fresh catalog for an explicitly capped spec.
// Uncapped specs preserve legacy behavior and never call the Sizes API.
func QuoteDropletPrice(ctx context.Context, client DropletSizesClient, spec interfaces.ResourceSpec) (*DropletPriceQuote, error) {
	value, capped := spec.Config["max_monthly_usd"]
	if !capped {
		return nil, nil
	}
	capUSD, capExact, err := dropletMonthlyCap(value)
	if err != nil {
		return nil, err
	}
	slug, slugOK := spec.Config["size"].(string)
	region, regionOK := spec.Config["region"].(string)
	if !slugOK || !validPriceSlug(slug) {
		return nil, dropletPricingInvalid("capped droplet requires an explicit size slug")
	}
	if !regionOK || !validPriceSlug(region) {
		return nil, dropletPricingInvalid("capped droplet requires an explicit region slug")
	}
	if isNilLike(client) {
		return nil, dropletPricingInvalid("live Sizes client is required for capped droplets")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var selected *godo.Size
	rows := 0
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, dropletPricingAPIError(err)
		}
		sizes, response, err := client.List(ctx, &godo.ListOptions{Page: page, PerPage: 200})
		if err != nil {
			return nil, dropletPricingAPIError(err)
		}
		rows += len(sizes)
		for _, size := range sizes {
			if size.Slug != slug {
				continue
			}
			if selected != nil {
				return nil, dropletPricingInvalid("live Sizes catalog contains an ambiguous size slug")
			}
			copy := size
			selected = &copy
		}
		next := ""
		if response != nil && response.Links != nil && response.Links.Pages != nil {
			next = response.Links.Pages.Next
		}
		if next == "" {
			if response != nil && response.Meta != nil && response.Meta.Total != rows {
				return nil, dropletPricingInvalid("live Sizes catalog is incomplete")
			}
			break
		}
		// Parse only the page number; never follow a server-supplied URL.
		// All requests continue through the injected Sizes client/base URL.
		nextURL, err := url.Parse(next)
		if err != nil {
			return nil, dropletPricingInvalid("live Sizes pagination is malformed")
		}
		nextPage, err := strconv.Atoi(nextURL.Query().Get("page"))
		if err != nil || nextPage != page+1 || page >= 100 {
			return nil, dropletPricingInvalid("live Sizes pagination is incomplete or cyclic")
		}
	}
	if selected == nil {
		return nil, dropletPricingInvalid("selected size is absent from the live Sizes catalog")
	}
	if !selected.Available || !slices.Contains(selected.Regions, region) {
		return nil, dropletPricingInvalid("selected size is not available in the selected region")
	}
	monthly := selected.PriceMonthly
	if monthly <= 0 || math.IsNaN(monthly) || math.IsInf(monthly, 0) {
		return nil, dropletPricingInvalid("selected size has an invalid monthly USD price")
	}
	// Use the SDK's monthly field, not an hourly estimate, and never round a
	// decimal cap upward before comparing it with the authoritative quote.
	monthlyExact, ok := new(big.Rat).SetString(strconv.FormatFloat(monthly, 'g', -1, 64))
	if !ok || monthlyExact.Cmp(capExact) > 0 {
		return nil, dropletPricingInvalid("selected size exceeds max_monthly_usd")
	}
	return &DropletPriceQuote{SizeSlug: slug, Region: region, MonthlyUSD: monthly, MaxMonthlyUSD: capUSD, Source: "digitalocean.sizes", CheckedAt: time.Now().UTC()}, nil
}

func validPriceSlug(slug string) bool {
	if slug == "" {
		return false
	}
	for _, ch := range slug {
		if ch != '-' && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func dropletMonthlyCap(value any) (float64, *big.Rat, error) {
	invalid := func() (float64, *big.Rat, error) {
		return 0, nil, dropletPricingInvalid("max_monthly_usd must be a positive finite number")
	}
	var number string
	if n, ok := value.(json.Number); ok {
		number = n.String()
		if !json.Valid([]byte(number)) {
			return invalid()
		}
	} else {
		rv := reflect.ValueOf(value)
		if !rv.IsValid() {
			return invalid()
		}
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			number = strconv.FormatInt(rv.Int(), 10)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			number = strconv.FormatUint(rv.Uint(), 10)
		case reflect.Float32:
			number = strconv.FormatFloat(rv.Float(), 'g', -1, 32)
		case reflect.Float64:
			number = strconv.FormatFloat(rv.Float(), 'g', -1, 64)
		default:
			return invalid()
		}
	}
	usd, err := strconv.ParseFloat(number, 64)
	if err != nil || usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return invalid()
	}
	exact, ok := new(big.Rat).SetString(number)
	if !ok || exact.Sign() <= 0 {
		return invalid()
	}
	return usd, exact, nil
}

func dropletPricingInvalid(message string) error {
	return fmt.Errorf("droplet pricing: %s: %w", message, interfaces.ErrValidation)
}

type dropletPricingFailure struct{ cause, classification error }

func (e *dropletPricingFailure) Error() string {
	if e.classification != nil {
		return "droplet pricing lookup: " + e.classification.Error()
	}
	return "droplet pricing lookup failed"
}

func (e *dropletPricingFailure) Unwrap() []error {
	if e.classification != nil {
		return []error{e.classification, e.cause}
	}
	return []error{e.cause}
}

func dropletPricingAPIError(cause error) error {
	var response *godo.ErrorResponse
	var classification error
	if errors.As(cause, &response) && response.Response != nil {
		classification = sentinelForStatus(response.Response.StatusCode)
	}
	return &dropletPricingFailure{cause: cause, classification: classification}
}
