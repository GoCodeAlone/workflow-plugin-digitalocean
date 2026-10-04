package drivers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type pricingSizesClient struct {
	sizes []godo.Size
	err   error
	calls int
	log   *callLog
}

func (c *pricingSizesClient) List(ctx context.Context, _ *godo.ListOptions) ([]godo.Size, *godo.Response, error) {
	c.calls++
	c.log.record("Sizes.List")
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return c.sizes, nil, c.err
}

func cappedDropletSpec() interfaces.ResourceSpec {
	return interfaces.ResourceSpec{Name: "host", Type: "infra.droplet", Config: map[string]any{"size": "s-2vcpu-4gb", "region": "nyc3", "max_monthly_usd": 24}}
}

func availableDropletSize(price float64) godo.Size {
	return godo.Size{Slug: "s-2vcpu-4gb", Regions: []string{"nyc3"}, Available: true, PriceMonthly: price}
}

func TestDropletPricing_GuardsDiffCreateAndReplaceBeforeMutation(t *testing.T) {
	for _, operation := range []string{"diff", "create", "replace"} {
		t.Run(operation, func(t *testing.T) {
			log := &callLog{}
			sizes := &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24.01)}, log: log}
			droplets := newRecordingDropletsClient().withCallLog(log)
			droplets.getResponse = &godo.Droplet{ID: 100, VolumeIDs: []string{"vol-a"}}
			sa := newRecordingStorageActionsClient().withCallLog(log)
			d := drivers.NewDropletDriverWithClient(droplets, "nyc3", sizes, sa, newRecordingActionsClient())
			var err error
			switch operation {
			case "diff":
				_, err = d.Diff(t.Context(), cappedDropletSpec(), nil)
			case "create":
				_, err = d.Create(t.Context(), cappedDropletSpec())
			case "replace":
				_, err = d.Replace(t.Context(), interfaces.ResourceRef{Name: "host", ProviderID: "100"}, cappedDropletSpec())
			}
			if err == nil {
				t.Fatal("over-cap profile accepted")
			}
			if droplets.createCalled || droplets.deleteCalled || len(sa.detachCalls) != 0 {
				t.Fatal("over-cap profile mutated cloud state")
			}
			if sizes.calls != 1 || len(log.events) != 1 || log.events[0] != "Sizes.List" {
				t.Fatal("pricing was not checked before driver execution")
			}
		})
	}
}

func TestDropletPricing_ExactLiveQuoteAtCap(t *testing.T) {
	sizes := &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}}
	before := cappedDropletSpec()
	quote, err := drivers.QuoteDropletPrice(t.Context(), sizes, before)
	if err != nil {
		t.Fatal(err)
	}
	if quote == nil || quote.SizeSlug != "s-2vcpu-4gb" || quote.Region != "nyc3" || quote.MonthlyUSD != 24 || quote.MaxMonthlyUSD != 24 || quote.Source != "digitalocean.sizes" || quote.CheckedAt.IsZero() || quote.CheckedAt.Location().String() != "UTC" {
		t.Fatal("incorrect live quote")
	}
	if len(before.Config) != 3 {
		t.Fatal("pricing rewrote declarative config")
	}
}

func TestDropletPricing_NoStaticOrCachedAuthorization(t *testing.T) {
	sizes := &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}}
	droplets := newRecordingDropletsClient()
	d := drivers.NewDropletDriverWithClient(droplets, "nyc3", sizes)
	if _, err := d.Diff(t.Context(), cappedDropletSpec(), nil); err != nil {
		t.Fatal(err)
	}
	sizes.sizes[0].PriceMonthly = 25
	if _, err := d.Create(t.Context(), cappedDropletSpec()); err == nil {
		t.Fatal("stale plan quote authorized create")
	}
	if droplets.createCalled || sizes.calls != 2 {
		t.Fatal("fresh pricing guard did not stop create")
	}
	sizes.sizes = nil
	if _, err := drivers.QuoteDropletPrice(t.Context(), sizes, cappedDropletSpec()); err == nil {
		t.Fatal("static known slug bypassed empty live catalog")
	}
}

func TestDropletPricing_ApplyRechecksPlanBeforeAnyMutation(t *testing.T) {
	for _, operation := range []string{"create", "replace"} {
		for _, failure := range []string{"changed_price", "unavailable", "api"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				log := &callLog{}
				sizes := &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}, log: log}
				droplets := newRecordingDropletsClient().withCallLog(log)
				droplets.getResponse = &godo.Droplet{ID: 100, VolumeIDs: []string{"vol-a"}}
				sa := newRecordingStorageActionsClient().withCallLog(log)
				d := drivers.NewDropletDriverWithClient(droplets, "nyc3", sizes, sa, newRecordingActionsClient())
				if _, err := d.Diff(t.Context(), cappedDropletSpec(), nil); err != nil {
					t.Fatal(err)
				}
				switch failure {
				case "changed_price":
					sizes.sizes[0].PriceMonthly = 25
				case "unavailable":
					sizes.sizes[0].Available = false
				case "api":
					sizes.err = errors.New("KNOWN_PRICE_SECRET")
				}
				var err error
				if operation == "create" {
					_, err = d.Create(t.Context(), cappedDropletSpec())
				} else {
					_, err = d.Replace(t.Context(), interfaces.ResourceRef{Name: "host", ProviderID: "100"}, cappedDropletSpec())
				}
				if err == nil || strings.Contains(err.Error(), "KNOWN_PRICE_SECRET") {
					t.Fatal("changed or failed lookup accepted or disclosed")
				}
				if sizes.err != nil && !errors.Is(err, sizes.err) {
					t.Fatal("lookup failure cause lost")
				}
				if droplets.createCalled || droplets.deleteCalled || len(sa.detachCalls) != 0 || !slices.Equal(log.events, []string{"Sizes.List", "Sizes.List"}) {
					t.Fatal("apply reused plan authorization or mutated before lookup")
				}
			})
		}
	}
}

func TestDropletPricing_SuccessfulApplyQuoteAndReplaceOrder(t *testing.T) {
	for _, operation := range []string{"create", "replace"} {
		t.Run(operation, func(t *testing.T) {
			log := &callLog{}
			sizes := &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}, log: log}
			droplets := newRecordingDropletsClient().withCallLog(log)
			droplets.getResponse = &godo.Droplet{ID: 100, VolumeIDs: []string{"vol-a"}}
			sa := newRecordingStorageActionsClient().withCallLog(log)
			d := drivers.NewDropletDriverWithClient(droplets, "nyc3", sizes, sa, newRecordingActionsClient().withCallLog(log))
			var out *interfaces.ResourceOutput
			var err error
			want := []string{"Sizes.List", "Droplets.Create"}
			if operation == "create" {
				out, err = d.Create(t.Context(), cappedDropletSpec())
			} else {
				out, err = d.Replace(t.Context(), interfaces.ResourceRef{Name: "host", ProviderID: "100"}, cappedDropletSpec())
				want = []string{"Sizes.List", "Droplets.Get", "StorageActions.DetachByDropletID", "Actions.Get", "Droplets.Delete", "Sizes.List", "Droplets.Create"}
			}
			if err != nil {
				t.Fatal(err)
			}
			quote, ok := out.Outputs["price_quote"].(*drivers.DropletPriceQuote)
			if !ok || quote.MonthlyUSD != 24 || quote.CheckedAt.IsZero() {
				t.Fatal("successful apply did not return typed live quote")
			}
			if !slices.Equal(log.events, want) {
				t.Fatalf("incorrect price/mutation ordering: %v", log.events)
			}
			if droplets.createReq.Size != "s-2vcpu-4gb" || droplets.createReq.Region != "nyc3" {
				t.Fatal("quote and create target disagree")
			}
		})
	}
}

func TestDropletPricing_InvalidCapsAndMissingSelectionBeforeAPI(t *testing.T) {
	sizes := &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}}
	for _, cap := range []any{nil, "24", true, -1, 0, math.NaN(), math.Inf(1), math.Inf(-1), json.Number("24/1"), json.Number("24junk"), json.Number("+24"), []int{24}, map[string]any{"password": "KNOWN_PRICE_SECRET"}} {
		spec := cappedDropletSpec()
		spec.Config["max_monthly_usd"] = cap
		_, err := drivers.QuoteDropletPrice(t.Context(), sizes, spec)
		if err == nil || strings.Contains(err.Error(), "KNOWN_PRICE_SECRET") {
			t.Fatal("invalid cap accepted or disclosed")
		}
	}
	for _, key := range []string{"size", "region"} {
		for _, value := range []any{nil, "", " ", "invalid/KNOWN_PRICE_SECRET", true} {
			spec := cappedDropletSpec()
			spec.Config[key] = value
			if _, err := drivers.QuoteDropletPrice(t.Context(), sizes, spec); err == nil || strings.Contains(err.Error(), "KNOWN_PRICE_SECRET") {
				t.Fatal("invalid selection accepted or disclosed")
			}
		}
		spec := cappedDropletSpec()
		delete(spec.Config, key)
		if _, err := drivers.QuoteDropletPrice(t.Context(), sizes, spec); err == nil {
			t.Fatal("missing selection accepted")
		}
	}
	if sizes.calls != 0 {
		t.Fatal("malformed inputs reached API")
	}
}

func TestDropletPricing_RejectsUnknownUnavailableRegionAndMalformedPrices(t *testing.T) {
	for _, tc := range []struct {
		name string
		size godo.Size
	}{
		{"unknown", godo.Size{Slug: "s-2vcpu-8gb", Available: true, Regions: []string{"nyc3"}, PriceMonthly: 24}},
		{"unavailable", godo.Size{Slug: "s-2vcpu-4gb", Regions: []string{"nyc3"}, PriceMonthly: 24}},
		{"region", godo.Size{Slug: "s-2vcpu-4gb", Available: true, Regions: []string{"sfo3"}, PriceMonthly: 24}},
		{"zero", availableDropletSize(0)}, {"negative", availableDropletSize(-1)},
		{"nan", availableDropletSize(math.NaN())}, {"infinite", availableDropletSize(math.Inf(1))},
		{"overcap", availableDropletSize(math.Nextafter(24, 25))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quote, err := drivers.QuoteDropletPrice(t.Context(), &pricingSizesClient{sizes: []godo.Size{tc.size}}, cappedDropletSpec())
			if err == nil || quote != nil {
				t.Fatal("invalid live size authorized")
			}
		})
	}
}

func TestDropletPricing_UncappedLegacyAndMissingClient(t *testing.T) {
	spec := cappedDropletSpec()
	delete(spec.Config, "max_monthly_usd")
	quote, err := drivers.QuoteDropletPrice(t.Context(), nil, spec)
	if err != nil || quote != nil {
		t.Fatal("uncapped legacy requires pricing")
	}
	droplets := newRecordingDropletsClient()
	if _, err := drivers.NewDropletDriverWithClient(droplets, "nyc3").Create(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if !droplets.createCalled {
		t.Fatal("legacy create changed")
	}
	for _, sizes := range []drivers.DropletSizesClient{nil, (*pricingSizesClient)(nil)} {
		if _, err := drivers.QuoteDropletPrice(t.Context(), sizes, cappedDropletSpec()); err == nil {
			t.Fatal("capped missing pricing client accepted")
		}
	}
}

func TestDropletPricing_GodoPaginationAndSanitizedFailures(t *testing.T) {
	for _, mode := range []string{"pages", "duplicate", "second_failure", "malformed", "forbidden", "cycle", "incomplete", "malformed_next"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/v2/sizes" || r.URL.Query().Get("per_page") != "200" {
					t.Error("unexpected sizes API request")
				}
				page := r.URL.Query().Get("page")
				if mode == "forbidden" || (mode == "second_failure" && page == "2") {
					w.WriteHeader(http.StatusForbidden)
					_, _ = fmt.Fprint(w, `{"message":"KNOWN_PRICE_SECRET"}`)
					return
				}
				if mode == "malformed" {
					_, _ = fmt.Fprint(w, `{"sizes":[{"price_monthly":"KNOWN_PRICE_SECRET"}]}`)
					return
				}
				rows := []godo.Size{availableDropletSize(24)}
				next := ""
				if page == "1" {
					next = "/v2/sizes?page=2&per_page=200"
					if mode == "pages" {
						rows = []godo.Size{{Slug: "s-other"}}
					}
					if mode == "cycle" {
						next = "/v2/sizes?page=1"
					}
					if mode == "malformed_next" {
						next = "/v2/sizes?page=UNKNOWN"
					}
					if mode == "incomplete" {
						_ = json.NewEncoder(w).Encode(map[string]any{"sizes": rows, "meta": map[string]any{"total": 2}})
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"sizes": rows, "links": map[string]any{"pages": map[string]any{"next": next}}})
			}))
			defer srv.Close()
			quote, err := drivers.QuoteDropletPrice(t.Context(), godoClientForTest(t, srv).Sizes, cappedDropletSpec())
			if mode == "pages" {
				if err != nil || quote == nil || calls != 2 {
					t.Fatalf("page traversal failed: %v", err)
				}
				return
			}
			if err == nil || quote != nil || strings.Contains(err.Error(), "KNOWN_PRICE_SECRET") {
				t.Fatal("unsafe/incomplete catalog authorized")
			}
			if mode == "forbidden" || mode == "second_failure" {
				var cause *godo.ErrorResponse
				if !errors.Is(err, interfaces.ErrForbidden) || !errors.As(err, &cause) {
					t.Fatal("API classification/cause lost")
				}
			}
			if mode == "cycle" && calls != 1 {
				t.Fatal("cyclic page link was followed")
			}
		})
	}
}

func TestDropletPricing_CanceledLookupAndExactNumericCaps(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := drivers.QuoteDropletPrice(ctx, &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}}, cappedDropletSpec())
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause lost")
	}
	for _, cap := range []any{24, int64(24), float64(24), float32(24), json.Number("2.4e1")} {
		spec := cappedDropletSpec()
		spec.Config["max_monthly_usd"] = cap
		if _, err := drivers.QuoteDropletPrice(t.Context(), &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}}, spec); err != nil {
			t.Fatal("valid numeric cap rejected")
		}
	}
	spec := cappedDropletSpec()
	spec.Config["max_monthly_usd"] = json.Number("23.999999999999999999")
	if _, err := drivers.QuoteDropletPrice(t.Context(), &pricingSizesClient{sizes: []godo.Size{availableDropletSize(24)}}, spec); err == nil {
		t.Fatal("rounded-up cap authorized over-cap profile")
	}
}
