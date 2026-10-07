package apperr

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReasonAndMetadataSurviveTransport(t *testing.T) {
	err := New(codes.Unavailable, ReasonOutcomeUnknown, "исход неизвестен", MetaTransactionID, "tx-1")

	// Как ошибка выглядит после передачи по gRPC: код, текст и детали восстанавливаются из proto.
	transported := status.FromProto(status.Convert(err).Proto()).Err()
	reason, meta := Info(transported)
	if reason != ReasonOutcomeUnknown || meta[MetaTransactionID] != "tx-1" || status.Code(transported) != codes.Unavailable {
		t.Fatalf("reason=%q meta=%v code=%v", reason, meta, status.Code(transported))
	}
	if Reason(status.Error(codes.DeadlineExceeded, "без деталей")) != ReasonTimeout {
		t.Fatal("для ошибки без деталей причина должна выводиться из кода")
	}
}

func TestIsDefinite(t *testing.T) {
	definite := []codes.Code{codes.InvalidArgument, codes.NotFound, codes.PermissionDenied, codes.FailedPrecondition, codes.AlreadyExists, codes.Aborted}
	unknown := []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.Unknown, codes.Canceled, codes.ResourceExhausted}
	for _, c := range definite {
		if !IsDefinite(status.Error(c, "")) {
			t.Errorf("%v должен считаться определённым отказом", c)
		}
	}
	for _, c := range unknown {
		if IsDefinite(status.Error(c, "")) {
			t.Errorf("%v: исход должен считаться неизвестным", c)
		}
	}
	if IsDefinite(fmt.Errorf("обёртка: %w", context.DeadlineExceeded)) {
		t.Error("дедлайн контекста — неизвестный исход")
	}
}

func TestCodeForReasonMatchesServices(t *testing.T) {
	cases := map[string]codes.Code{
		ReasonInsufficientFunds:   codes.FailedPrecondition,
		ReasonAccountForbidden:    codes.PermissionDenied,
		ReasonRecipientNotFound:   codes.NotFound,
		ReasonConcurrencyConflict: codes.Aborted,
		ReasonTransferAborted:     codes.FailedPrecondition,
		ReasonOverloaded:          codes.ResourceExhausted,
	}
	for reason, want := range cases {
		if got := CodeForReason(reason); got != want {
			t.Errorf("CodeForReason(%s) = %v, want %v", reason, got, want)
		}
	}
}
