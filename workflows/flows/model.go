package flows

import (
	"cmp"
	"errors"
	"net/http"
	"os"

	"github.com/digitallysavvy/go-ai/pkg/provider"
	"github.com/digitallysavvy/go-ai/pkg/providers/openai"
)

// gatewayURL is the AI Gateway's OpenAI-compatible API. go-ai's gateway
// provider sends an outdated request body that the gateway rejects with
// "Invalid input", so Task Manager talks to the gateway through this API instead.
//
// TODO: switch back to go-ai's gateway provider once its next release
// includes https://github.com/digitallysavvy/go-ai/pull/86.
const gatewayURL = "https://ai-gateway.vercel.sh/v1"

// NewModel returns the model named by MODEL through the AI Gateway's
// OpenAI-compatible Chat Completions API, which authenticates with
// AI_GATEWAY_API_KEY or the deployment's OIDC token. The token is read on
// every call, not here: on Vercel it arrives with each request, so it may
// not be set yet when the model is built.
func NewModel() (provider.LanguageModel, error) {
	gw := openai.New(openai.Config{
		Name:    "gateway",
		BaseURL: gatewayURL,
		// The transport sets the real token on each request.
		APIKey:     "gateway",
		HTTPClient: &http.Client{Transport: gatewayAuth{next: http.DefaultTransport}},
	})
	return gw.ChatModel(cmp.Or(os.Getenv("MODEL"), "openai/gpt-5-mini"))
}

// errNoGatewayToken is returned by a model call when there is no token.
var errNoGatewayToken = errors.New("set AI_GATEWAY_API_KEY, or deploy on Vercel or run vercel env pull for VERCEL_OIDC_TOKEN")

// HasGatewayToken reports whether a model call has a token to send.
func HasGatewayToken() bool { return gatewayToken() != "" }

// gatewayToken returns AI_GATEWAY_API_KEY, or else the OIDC token Vercel
// rotates, read anew on every call.
func gatewayToken() string {
	return cmp.Or(os.Getenv("AI_GATEWAY_API_KEY"), os.Getenv("VERCEL_OIDC_TOKEN"))
}

// gatewayAuth sends each request with the current gateway token, so a
// cached model keeps working after Vercel rotates the OIDC token.
type gatewayAuth struct{ next http.RoundTripper }

func (t gatewayAuth) RoundTrip(request *http.Request) (*http.Response, error) {
	token := gatewayToken()
	if token == "" {
		if request.Body != nil {
			request.Body.Close()
		}
		return nil, errNoGatewayToken
	}
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+token)
	return t.next.RoundTrip(request)
}
