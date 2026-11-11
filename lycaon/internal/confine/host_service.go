package confine

// HostService is a socket the host serves to one action, such as the
// credential service an agent's Git asks. Unlike a user's socket grant it is
// never relayed: the service must observe the confined client as its peer.
type HostService struct {
	// path is admitted by an exact kernel path rule.
	path string
	// grant is the retained object a connect mediator dials instead.
	grant *SocketGrant
}

func hostServiceAdmissions(services []HostService) (grants []SocketGrant, paths []string) {
	for _, service := range services {
		switch {
		case service.grant != nil:
			grants = append(grants, *service.grant)
		case service.path != "":
			paths = append(paths, service.path)
		}
	}
	return grants, paths
}
