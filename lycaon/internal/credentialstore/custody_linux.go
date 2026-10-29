package credentialstore

func describeCustody(path string) (CustodyReport, error) { return linuxCustody().report(path) }

func (c custody) report(string) (CustodyReport, error) {
	switch c.mode {
	case custodyRefused:
		return CustodyReport{Mode: CustodyRefused, Detail: c.refusal.Error()}, nil
	case custodyService:
		return CustodyReport{Mode: CustodyIdentityService, ServiceVerified: true}, nil
	default:
		return CustodyReport{Mode: CustodyUnavailable}, nil
	}
}
