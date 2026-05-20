package internal

import (
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
)

func (s *doIaCServer) CaptureLogs(req *pb.CaptureLogsRequest, stream pb.IaCProviderLogCapture_CaptureLogsServer) error {
	return s.provider.CaptureLogs(stream.Context(), logCaptureRequestFromPB(req), streamLogSink{stream: stream})
}

func logCaptureRequestFromPB(req *pb.CaptureLogsRequest) interfaces.LogCaptureRequest {
	if req == nil {
		return interfaces.LogCaptureRequest{}
	}
	return interfaces.LogCaptureRequest{
		ResourceName:    req.GetResourceName(),
		ResourceType:    req.GetResourceType(),
		ProviderID:      req.GetProviderId(),
		ComponentName:   req.GetComponentName(),
		LogType:         logCaptureTypeFromPB(req.GetLogType()),
		TailLines:       int(req.GetTailLines()),
		Follow:          req.GetFollow(),
		DurationSeconds: req.GetDurationSeconds(),
		DeploymentID:    req.GetDeploymentId(),
	}
}

func logCaptureTypeFromPB(t pb.LogCaptureType) string {
	switch t {
	case pb.LogCaptureType_LOG_CAPTURE_TYPE_BUILD:
		return "BUILD"
	case pb.LogCaptureType_LOG_CAPTURE_TYPE_DEPLOY:
		return "DEPLOY"
	case pb.LogCaptureType_LOG_CAPTURE_TYPE_RUN_RESTARTED:
		return "RUN_RESTARTED"
	default:
		return "RUN"
	}
}

type streamLogSink struct {
	stream pb.IaCProviderLogCapture_CaptureLogsServer
}

func (s streamLogSink) WriteLogChunk(chunk interfaces.LogChunk) error {
	if s.stream == nil {
		return fmt.Errorf("digitalocean CaptureLogs: nil stream")
	}
	return s.stream.Send(&pb.LogChunk{
		Data:   append([]byte(nil), chunk.Data...),
		Source: chunk.Source,
		Eof:    chunk.EOF,
	})
}

var _ interfaces.LogCaptureSink = (*discardLogSink)(nil)

type discardLogSink struct{}

func (*discardLogSink) WriteLogChunk(interfaces.LogChunk) error { return nil }
