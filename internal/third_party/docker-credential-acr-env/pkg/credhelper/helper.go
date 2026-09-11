// Package credhelper provides the interface expected by Cosign's CLI options.
//
// Hauler does not use Cosign's optional ACR credential helper. The upstream
// implementation has a credential-leak vulnerability and no fixed release,
// so this replacement deliberately fails closed instead of contacting ACR.
package credhelper

import (
	"errors"

	"github.com/docker/docker-credential-helpers/credentials"
)

var errDisabled = errors.New("Azure Container Registry credential helper is disabled in Hauler")

type disabledHelper struct{}

func NewACRCredentialsHelper() credentials.Helper { return disabledHelper{} }

func (disabledHelper) Add(*credentials.Credentials) error { return errDisabled }
func (disabledHelper) Delete(string) error                { return errDisabled }
func (disabledHelper) Get(string) (string, string, error) { return "", "", errDisabled }
func (disabledHelper) List() (map[string]string, error)   { return nil, errDisabled }
