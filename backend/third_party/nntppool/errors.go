package nntppool

import (
	"errors"
	"io"
	"net"
	"net/textproto"
	"syscall"

	"golang.org/x/text/transform"

	"github.com/javi11/nntppool/v2/pkg/nntpcli"
)

var (
	ErrCapabilitiesUnpopulated    = errors.New("capabilities unpopulated")
	ErrNoSuchCapability           = errors.New("no such capability")
	ErrNilNntpConn                = errors.New("nil nntp connection")
	ErrNoProviderAvailable        = errors.New("no provider available, because possible max connections reached")
	ErrArticleNotFoundInProviders = errors.New("the article is not found in any of your providers")
	ErrFailedToPostInAllProviders = errors.New("failed to post in all providers")
	ErrConnectionPoolShutdown     = errors.New("connection pool is shutdown")
)

const (
	SegmentAlreadyExistsErrCode = 441
	ToManyConnectionsErrCode    = 502
	ArticleNotFoundErrCode      = 430
	CanNotJoinGroup             = 411
	AuthenticationRequiredCode  = 401
	AuthenticationFailedCode    = 403
	InvalidUsernamePasswordCode = 480
)

var retirableErrors = []int{
	SegmentAlreadyExistsErrCode,
	ToManyConnectionsErrCode,
	CanNotJoinGroup,
	ArticleNotFoundErrCode,
}

func isRetryableError(err error) bool {
	if errors.Is(err, ErrNilNntpConn) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, io.ErrShortWrite) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, transform.ErrShortSrc) {
		return true
	}

	var netErr net.Error
	if ok := errors.As(err, &netErr); ok {
		return true
	}

	var protocolErr textproto.ProtocolError
	if ok := errors.As(err, &protocolErr); ok {
		return true
	}

	var nntpErr *textproto.Error
	if ok := errors.As(err, &nntpErr); ok && isRetirableCode(nntpErr.Code) {
		return true
	}

	// The vendored nntpcli reports server status lines as *nntpcli.NNTPError,
	// not *textproto.Error. Without this check a 430 from the first provider is
	// never retried, so the pool never falls over to the next provider.
	var cliErr *nntpcli.NNTPError
	if ok := errors.As(err, &cliErr); ok && isRetirableCode(cliErr.Code) {
		return true
	}

	return false
}

func isRetirableCode(code int) bool {
	for _, r := range retirableErrors {
		if code == r {
			return true
		}
	}
	return false
}
