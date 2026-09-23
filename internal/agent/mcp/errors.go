package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// Error 保留底层 cause 供内部诊断，同时只向界面暴露固定的安全文案。
type Error struct {
	message string
	stage   string
	reason  string
	cause   error
}

func (e *Error) Error() string { return e.message }
func (e *Error) Unwrap() error { return e.cause }

// Diagnostic 返回可安全记录的阶段和原因，不返回可能包含 URL、命令或响应正文的 cause。
func Diagnostic(err error) (stage, reason string) {
	var target *Error
	if errors.As(err, &target) {
		return target.stage, target.reason
	}
	return "connect", classifyReason(err)
}

func newError(message, stage string, cause error) error {
	return &Error{message: message, stage: stage, reason: classifyReason(cause), cause: cause}
}

func newProtocolError(message, stage string, cause error) error {
	return &Error{message: message, stage: stage, reason: "protocol", cause: cause}
}

func classifyReason(err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "not_found"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var tlsErr *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var record tls.RecordHeaderError
	if errors.As(err, &tlsErr) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &certificate) || errors.As(err, &record) {
		return "tls"
	}
	var rpcErr *jsonrpc.Error
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var protocolErr *http.ProtocolError
	if errors.As(err, &rpcErr) || errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.As(err, &protocolErr) {
		return "protocol"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "unknown"
}
