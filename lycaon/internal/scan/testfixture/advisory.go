package testfixture

import (
	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/pkg/api"
)

func WithAdvisory(finding api.SecurityFinding, ids ...string) api.SecurityFinding {
	if finding.Properties == nil {
		finding.Properties = &api.SecurityFindingProperties{}
	}
	if finding.Properties.Lycaon == nil {
		finding.Properties.Lycaon = &api.SecurityFindingLycaonProperties{}
	}
	finding.Properties.Lycaon.Advisory = &api.AdvisoryRef{OSVID: ids[0], CVEIDs: ids[1:]}
	return finding
}

// AdvisoryWithPackage builds an advisory reference for one affected package.
func AdvisoryWithPackage(id, name, version, ecosystem string) *api.AdvisoryRef {
	adv := advisory.BuildAdvisoryRef(id)
	adv.Package = &api.AdvisoryPackageRef{Name: name, Version: version, Ecosystem: ecosystem}
	return adv
}
