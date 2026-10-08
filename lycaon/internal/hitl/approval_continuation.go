package hitl

import ()

func (p ApprovalPlan) OptionContinues(option ApprovalOption) bool {
	if option.Kind == ApprovalOptionRedacted || option.Kind == ApprovalOptionTracked {
		return p.Subject.Kind == ApprovalSubjectSecret
	}
	if p.Subject.Kind == ApprovalSubjectActionSet {
		return actionSetOptionContinues(p.Subject.Targets, option.Authority)
	}
	for _, delta := range option.Authority {
		switch p.Subject.Kind {
		case ApprovalSubjectAction, ApprovalSubjectDestinationSet, ApprovalSubjectSecret, ApprovalSubjectPackageSet, ApprovalSubjectProcessControl, ApprovalSubjectHostExecution:
			if delta.Kind == AuthorityCurrentAction || delta.Kind == AuthorityGenericGrant {
				return true
			}
		case ApprovalSubjectSocketSet:
			if delta.Kind == AuthoritySocketPermit || delta.Kind == AuthoritySocketChat {
				return true
			}
		case ApprovalSubjectDirectIP:
			if delta.Kind == AuthorityDirectIPPermit || delta.Kind == AuthorityDirectIPChat {
				return true
			}
		case ApprovalSubjectLocalListen:
			if delta.Kind == AuthorityLocalListenChat || delta.Kind == AuthorityCurrentAction {
				return true
			}
		case ApprovalSubjectLoopbackConnect:
			if delta.Kind == AuthorityLoopbackConnectChat || delta.Kind == AuthorityCurrentAction {
				return true
			}
		case ApprovalSubjectWriteRootSet:
			if delta.Kind == AuthorityWriteRootChat || delta.Kind == AuthorityGrantedPath {
				return true
			}
		case ApprovalSubjectReadPathSet:
			if delta.Kind == AuthorityReadPathChat {
				return true
			}
		case ApprovalSubjectActionSet:
		}
	}
	return false
}

func actionSetOptionContinues(targets []ApprovalTarget, authority []ApprovalAuthorityDelta) bool {
	required := map[string]bool{}
	for _, target := range targets {
		switch target.Kind {
		case "socket":
			required["socket"] = true
		case "direct_ip":
			required["direct_ip"] = true
		case "local_listen":
			required["local_listen"] = true
		case "loopback_connect":
			required["loopback_connect"] = true
		case "secret":
			required["secret"] = true
		case "write_root", "credential_file", "key_material":
			required["write_path"] = true
		case "read_path":
			required["read_path"] = true
		default:
			required["action"] = true
		}
	}
	covered := map[string]bool{}
	for _, delta := range authority {
		switch delta.Kind {
		case AuthorityCurrentAction, AuthorityGenericGrant:
			if delta.Kind == AuthorityGenericGrant && delta.Grant != nil && delta.Grant.Predicate.Category == ApprovalGrantCategorySecret {
				covered["secret"] = true
				continue
			}
			if delta.Kind == AuthorityCurrentAction {
				covered["secret"] = true
			}
			covered["action"] = true
			// Current-action covers both local-network axes on this spawn.
			covered["local_listen"] = true
			covered["loopback_connect"] = true
		case AuthoritySocketPermit, AuthoritySocketChat:
			covered["socket"] = true
		case AuthorityDirectIPPermit, AuthorityDirectIPChat:
			covered["direct_ip"] = true
		case AuthorityLocalListenChat:
			covered["local_listen"] = true
		case AuthorityLoopbackConnectChat:
			covered["loopback_connect"] = true
		case AuthorityWriteRootChat, AuthorityGrantedPath:
			covered["write_path"] = true
		case AuthorityReadPathChat:
			covered["read_path"] = true
		case AuthorityAskQuiet, AuthorityTrustDestination:
		}
	}
	if len(required) == 0 {
		return false
	}
	for axis := range required {
		if !covered[axis] {
			return false
		}
	}
	return true
}
