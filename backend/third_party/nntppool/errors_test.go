package nntppool

import (
	"fmt"
	"net/textproto"
	"testing"

	"github.com/javi11/nntppool/v2/pkg/nntpcli"
)

func TestIsRetryableErrorRecognisesNNTPCliArticleNotFound(t *testing.T) {
	wrapped := fmt.Errorf("error getting body reader: %w", &nntpcli.NNTPError{Code: 430, Msg: "No such article"})
	if !isRetryableError(wrapped) {
		t.Fatal("a 430 from nntpcli must be retryable so the pool tries the next provider")
	}
	if !isRetryableError(&textproto.Error{Code: 430}) {
		t.Fatal("textproto 430 must stay retryable")
	}
	if isRetryableError(&nntpcli.NNTPError{Code: 481, Msg: "auth"}) {
		t.Fatal("non-retirable codes must not be retried")
	}
}
