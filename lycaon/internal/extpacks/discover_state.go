package extpacks

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// DeviceState is parsed installation state.
type DeviceState struct {
	Desired DesiredState
	Lock    LockFile
}

// ParseDesired strictly decodes one desired-state document.
func ParseDesired(source string, data []byte) (DesiredState, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d DesiredState
	if err := dec.Decode(&d); err != nil {
		return DesiredState{}, fmt.Errorf("%s: %w", source, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return DesiredState{}, fmt.Errorf("%s: multiple YAML documents are not allowed", source)
		}
		return DesiredState{}, fmt.Errorf("%s: %w", source, err)
	}
	if d.Own == nil {
		d.Own = map[string]string{}
	}
	if err := ValidateDesired(d); err != nil {
		return DesiredState{}, fmt.Errorf("%s: %w", source, err)
	}
	return d, nil
}

// ParseLock strictly decodes one lock document.
func ParseLock(source string, data []byte) (LockFile, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var lock LockFile
	if err := dec.Decode(&lock); err != nil {
		return LockFile{}, fmt.Errorf("%s: %w", source, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return LockFile{}, fmt.Errorf("%s: multiple YAML documents are not allowed", source)
		}
		return LockFile{}, fmt.Errorf("%s: %w", source, err)
	}
	if err := ValidateLock(lock); err != nil {
		return LockFile{}, fmt.Errorf("%s: %w", source, err)
	}
	normalizeLock(&lock)
	return lock, nil
}

// DiscoverContentForState builds a candidate from device state and project disables.
func DiscoverContentForState(device DeviceState, projectDisabled []string) ([]PackContent, DesiredState, DesiredProvenance, error) {
	merged, prov := MergeDesired(device.Desired, projectDisabled)
	deviceLock := device.Lock
	normalizeLock(&deviceLock)
	if err := ValidateLock(deviceLock); err != nil {
		return nil, DesiredState{}, DesiredProvenance{}, fmt.Errorf("device lock: %w", err)
	}
	if err := validateDesiredLock(merged, deviceLock); err != nil {
		return nil, DesiredState{}, DesiredProvenance{}, err
	}
	deviceLock, err := reachableLock(deviceLock, merged)
	if err != nil {
		return nil, DesiredState{}, DesiredProvenance{}, err
	}
	stock, err := DiscoverStockContent()
	if err != nil {
		return nil, DesiredState{}, DesiredProvenance{}, err
	}
	content := make([]PackContent, 0, len(stock)+len(deviceLock.Packages))
	seen := map[string]bool{}
	for _, pc := range stock {
		content = append(content, pc)
		seen[pc.Pack.ID] = true
	}
	for _, locked := range deviceLock.Packages {
		if seen[locked.ID] {
			continue
		}
		content = append(content, admitLockedPackage(locked))
	}
	return content, merged, prov, nil
}
