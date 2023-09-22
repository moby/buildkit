/*
Wrapper APIs for in-toto attestation ResourceDescriptor protos.
*/

package v1

import "errors"

var ErrRDRequiredField = errors.New("at least one of name, URI, or digest are required")

func (d *ResourceDescriptor) Validate() error {
	// at least one of name, URI or digest are required
	if d.GetName() == "" && d.GetUri() == "" && len(d.GetDigest()) == 0 {
		return ErrRDRequiredField
	}

	return nil
}
