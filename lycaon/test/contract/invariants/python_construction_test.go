package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
)

const runtimeConstructionFixture = `package pythonruntime
type Runtime interface { Execute() }
type backend struct{}
func (*backend) Execute() {}
func AlternateBackend() *backend { return &backend{} }
type verifiedImage struct{}
func verifyImageInternal() *verifiedImage { return &verifiedImage{} }
func construct(v *verifiedImage) Runtime { return AlternateBackend() }
func NewVerifiedRuntime() Runtime { return construct(verifyImageInternal()) }
`

func TestPythonRuntimeConstructionBoundaries(t *testing.T) {
	const consumer = `package portablepython
import rt "github.com/lycaon/lycaon/internal/pythonruntime"
func NewService() rt.Runtime { return rt.NewVerifiedRuntime() }
`
	for _, tc := range []struct {
		name, rule, runtimeExtra, consumerExtra string
		want                                    outcome
	}{
		{"verified door", "portable-python-runtime-construction-verifies-the-image", "", "", passes},
		{"alternate factory as value", "portable-python-runtime-construction-verifies-the-image", "", "var selected = rt.AlternateBackend", violates},
		{"private helper", "portable-python-verification-result-is-construction-local", "", "", passes},
		{"public argument", "portable-python-verification-result-is-construction-local", "func Reuse(v *verifiedImage) {}", "", violates},
		{"retained by service", "portable-python-verification-result-is-construction-local", "type service struct { prior []*verifiedImage }", "", violates},
		{"inferred cache", "portable-python-verification-result-is-construction-local", "var cached = verifyImageInternal()", "", violates},
		{"alias", "portable-python-verification-result-is-construction-local", "type Prior = verifiedImage", "", violates},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"lycaon/internal/pythonruntime/backend.go":  runtimeConstructionFixture + tc.runtimeExtra,
				"lycaon/internal/portablepython/service.go": consumer + tc.consumerExtra,
			}
			result := invariants.Evaluate(fixture(t, files), rule(t, forbiddencalls.Family, tc.rule))
			expect(t, result, tc.want, 1)
		})
	}
}
