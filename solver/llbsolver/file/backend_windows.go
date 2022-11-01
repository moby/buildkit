package file

import (
	"github.com/docker/docker/pkg/idtools"
	copy "github.com/tonistiigi/fsutil/copy"
)

func mapUserToChowner(user *copy.User, idmap *idtools.IdentityMapping) (copy.Chowner, error) {
	var copyUser copy.User
	if user == nil || user.SID == "" {
		copyUser.SID = idtools.ContainerAdministratorSidString
	} else {
		copyUser.SID = user.SID
	}

	return func(*copy.User) (*copy.User, error) {
		return &copyUser, nil
	}, nil
}
