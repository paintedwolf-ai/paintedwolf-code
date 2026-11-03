package linuxbus

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const credentialMessageBytes = 4 << 20

var errCredentialWireLimit = errors.New("credential IPC exceeds its wire bound")

// readCredentialFrame admits the complete wire allocation before decoding it.
func readCredentialFrame(reader io.Reader) ([]byte, error) {
	var header [16]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	var order binary.ByteOrder
	switch header[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("invalid credential IPC byte order")
	}
	if header[3] != 1 {
		return nil, fmt.Errorf("invalid credential IPC version")
	}
	body, fields := uint64(order.Uint32(header[4:8])), uint64(order.Uint32(header[12:16]))
	total := ((16 + fields + 7) &^ uint64(7)) + body
	if total > credentialMessageBytes {
		return nil, errCredentialWireLimit
	}
	frame := make([]byte, int(total))
	copy(frame, header[:])
	_, err := io.ReadFull(reader, frame[len(header):])
	return frame, err
}

func readCredentialMessage(reader io.Reader) (*dbus.Message, error) {
	frame, err := readCredentialFrame(reader)
	if err != nil {
		return nil, err
	}
	return decodeCredentialFrame(frame)
}

func decodeCredentialFrame(frame []byte) (*dbus.Message, error) {
	message, err := dbus.DecodeMessage(bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	if message.Serial() == 0 {
		return nil, fmt.Errorf("credential IPC request has no serial")
	}
	if fds, ok := message.Headers[dbus.FieldUnixFDs]; ok && fds.Value() != uint32(0) {
		return nil, fmt.Errorf("credential IPC cannot pass file descriptors")
	}
	if credentialValueHasFD(reflect.ValueOf(message.Body)) {
		return nil, fmt.Errorf("credential IPC cannot encode file descriptors")
	}
	return message, nil
}

func writeCredentialMessage(writer io.Writer, message *dbus.Message, serial uint32) error {
	if credentialValueHasFD(reflect.ValueOf(message.Body)) {
		return fmt.Errorf("credential IPC cannot encode file descriptors")
	}
	var encoded bytes.Buffer
	if err := message.EncodeTo(&encoded, binary.LittleEndian); err != nil {
		return err
	}
	if encoded.Len() > credentialMessageBytes || serial == 0 {
		return errCredentialWireLimit
	}
	// Message's serial is private to godbus; this is the serial field of its
	// validated wire header, independent of the caller's reply serial.
	binary.LittleEndian.PutUint32(encoded.Bytes()[8:12], serial)
	n, err := writer.Write(encoded.Bytes())
	if err == nil && n != encoded.Len() {
		return io.ErrShortWrite
	}
	return err
}

func credentialServerAuth(reader *bufio.Reader, writer io.Writer, uid int, guid string) error {
	if first, err := reader.ReadByte(); err != nil || first != 0 {
		return fmt.Errorf("credential IPC requires local authentication")
	}
	if decoded, err := hex.DecodeString(guid); err != nil || len(decoded) != 16 {
		return fmt.Errorf("invalid credential IPC bus identity")
	}
	authenticated := false
	for range 16 {
		line, err := reader.ReadSlice('\n')
		if err != nil || len(line) > 1024 || !bytes.HasSuffix(line, []byte("\r\n")) {
			return fmt.Errorf("invalid credential IPC authentication line")
		}
		parts := strings.Split(string(line[:len(line)-2]), " ")
		reply := "ERROR Unsupported authentication command\r\n"
		switch parts[0] {
		case "AUTH":
			authenticated = false
			if len(parts) >= 2 && len(parts) <= 3 && parts[1] == "EXTERNAL" {
				identity := ""
				if len(parts) == 3 {
					decoded, decodeErr := hex.DecodeString(parts[2])
					if decodeErr != nil {
						return fmt.Errorf("invalid credential IPC authentication identity")
					}
					identity = string(decoded)
				}
				authenticated = identity == "" || identity == strconv.Itoa(uid)
			}
			reply = "REJECTED EXTERNAL\r\n"
			if authenticated {
				reply = "OK " + guid + "\r\n"
			}
		case "BEGIN":
			if authenticated && len(parts) == 1 {
				return nil
			}
		case "CANCEL":
			authenticated = false
			reply = "REJECTED EXTERNAL\r\n"
		case "NEGOTIATE_UNIX_FD":
			reply = "ERROR File descriptor passing is unavailable\r\n"
		}
		if _, err := io.WriteString(writer, reply); err != nil {
			return err
		}
	}
	return fmt.Errorf("credential IPC authentication exceeds its exchange bound")
}

func credentialValueHasFD(value reflect.Value) bool {
	if !value.IsValid() {
		return false
	}
	if value.CanInterface() {
		switch item := value.Interface().(type) {
		case dbus.UnixFD, dbus.UnixFDIndex:
			return true
		case dbus.Variant:
			return credentialValueHasFD(reflect.ValueOf(item.Value()))
		}
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		return !value.IsNil() && credentialValueHasFD(value.Elem())
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if credentialValueHasFD(value.Index(i)) {
				return true
			}
		}
	case reflect.Map:
		entries := value.MapRange()
		for entries.Next() {
			if credentialValueHasFD(entries.Key()) || credentialValueHasFD(entries.Value()) {
				return true
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if credentialValueHasFD(value.Field(i)) {
				return true
			}
		}
	}
	return false
}
