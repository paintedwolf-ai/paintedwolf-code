package inference

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
)

var ErrIdentity = errors.New("inference: unavailable physical device identity")

// PhysicalDevice identifies hardware independently of a runtime's device ordinals.
type PhysicalDevice struct {
	Kind    string `json:"kind"`
	Address string `json:"address,omitempty"`
}

var pciAddress = regexp.MustCompile(`^([0-9a-fA-F]{4,8}):([0-9a-fA-F]{2}):([0-9a-fA-F]{2})\.([0-7])$`)

func (p PhysicalDevice) key() (string, error) {
	switch p.Kind {
	case "cpu", "apple_silicon":
		if p.Address == "" {
			return p.Kind, nil
		}
	case "pci":
		parts := pciAddress.FindStringSubmatch(p.Address)
		if parts == nil {
			break
		}
		var numbers [4]uint64
		for i := range numbers {
			numbers[i], _ = strconv.ParseUint(parts[i+1], 16, 32)
		}
		if numbers[2] <= 31 {
			return fmt.Sprintf("pci:%04x:%02x:%02x.%x", numbers[0], numbers[1], numbers[2], numbers[3]), nil
		}
	}
	return "", ErrIdentity
}

// Devices shares compute admission across adapters in one host.
type Devices struct {
	mu         sync.Mutex
	queueLimit int
	closed     bool
	devices    map[string]*Device
}

func NewDevices(queueLimit int) (*Devices, error) {
	if queueLimit < 1 {
		return nil, errors.New("inference: queue limit must be positive")
	}
	return &Devices{queueLimit: queueLimit, devices: make(map[string]*Device)}, nil
}

func (d *Devices) Bind(identity PhysicalDevice) (*Device, error) {
	key, err := identity.key()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrClosed
	}
	if existing := d.devices[key]; existing != nil {
		return existing, nil
	}
	device, err := NewDevice(d.queueLimit)
	if err != nil {
		return nil, err
	}
	d.devices[key] = device
	return device, nil
}

func (d *Devices) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, device := range d.devices {
		device.Close()
	}
}
