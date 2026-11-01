package linuxbus

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/testutil"
)

func credentialWireFixture(t *testing.T, order binary.ByteOrder, body ...any) []byte {
	t.Helper()
	message := &dbus.Message{Type: dbus.TypeMethodCall, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldDestination: dbus.MakeVariant("org.freedesktop.secrets"),
		dbus.FieldPath:        dbus.MakeVariant(dbus.ObjectPath("/org/freedesktop/secrets")),
		dbus.FieldInterface:   dbus.MakeVariant("org.freedesktop.Secret.Service"),
		dbus.FieldMember:      dbus.MakeVariant("OpenSession"),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
	}, Body: body}
	var buffer bytes.Buffer
	testutil.FailErr(t, "encode protocol fixture", message.EncodeTo(&buffer, order))
	order.PutUint32(buffer.Bytes()[8:12], 41)
	return buffer.Bytes()
}

func TestCredentialFramesAdmitBothByteOrdersAndRejectDeclaredExpansion(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		frame := credentialWireFixture(t, order, "plain", dbus.MakeVariant(""))
		message, err := readCredentialMessage(bytes.NewReader(frame))
		testutil.FailErr(t, "read protocol frame", err)
		if message.Serial() != 41 || len(message.Body) != 2 || message.Body[0] != "plain" {
			t.Fatalf("wire identity changed: %+v", message)
		}
		for _, field := range []int{4, 12} {
			for _, size := range []uint32{credentialMessageBytes, credentialMessageBytes + 1, ^uint32(0)} {
				header := append([]byte(nil), frame[:16]...)
				order.PutUint32(header[field:field+4], size)
				if _, err := readCredentialFrame(bytes.NewReader(header)); !errors.Is(err, errCredentialWireLimit) {
					t.Fatalf("unadmitted allocation field=%d size=%d: %v", field, size, err)
				}
			}
		}
		for _, truncated := range [][]byte{frame[:3], frame[:15], frame[:len(frame)-1]} {
			if _, err := readCredentialMessage(bytes.NewReader(truncated)); err == nil {
				t.Fatal("accepted truncated IPC frame")
			}
		}
	}
}

func TestCredentialFramesRejectDescriptorsInNestedValues(t *testing.T) {
	for _, body := range [][]any{
		{dbus.UnixFDIndex(0)},
		{dbus.MakeVariant(dbus.UnixFDIndex(0))},
		{map[string]dbus.Variant{"descriptor": dbus.MakeVariant(dbus.UnixFDIndex(0))}},
		{[]dbus.Variant{dbus.MakeVariant(dbus.MakeVariant(dbus.UnixFDIndex(0)))}},
	} {
		frame := credentialWireFixture(t, binary.LittleEndian, body...)
		if _, err := readCredentialMessage(bytes.NewReader(frame)); err == nil {
			t.Fatalf("accepted descriptor-bearing body type %T", body[0])
		}
	}
}

func TestCredentialAuthenticationRequiresExternalIdentityWithoutDescriptorNegotiation(t *testing.T) {
	const guid = "0123456789abcdef0123456789abcdef"
	uid := hex.EncodeToString([]byte("1234"))
	for _, commands := range []string{
		"AUTH EXTERNAL " + uid + "\r\nBEGIN\r\n",
		"AUTH\r\nAUTH EXTERNAL " + uid + "\r\nNEGOTIATE_UNIX_FD\r\nBEGIN\r\n",
	} {
		var replies bytes.Buffer
		err := credentialServerAuth(bufio.NewReaderSize(strings.NewReader("\x00"+commands), 1024), &replies, 1234, guid)
		testutil.FailErr(t, "authenticate fixture peer", err)
		if !strings.Contains(replies.String(), "OK "+guid+"\r\n") || strings.Contains(replies.String(), "AGREE_UNIX_FD") {
			t.Fatalf("invalid authentication contract: %q", replies.String())
		}
	}
	for i, commands := range []string{
		"AUTH ANONYMOUS\r\nBEGIN\r\n",
		"AUTH EXTERNAL " + hex.EncodeToString([]byte("4321")) + "\r\nBEGIN\r\n",
		"AUTH EXTERNAL zz\r\nBEGIN\r\n",
		"BEGIN\r\n",
		"AUTH " + strings.Repeat("x", 4096) + "\r\n",
		strings.Repeat("AUTH\r\n", 17),
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			var replies bytes.Buffer
			if err := credentialServerAuth(bufio.NewReaderSize(strings.NewReader("\x00"+commands), 1024), &replies, 1234, guid); err == nil || strings.Contains(replies.String(), "OK ") {
				t.Fatalf("accepted invalid authentication: %q (%v)", replies.String(), err)
			}
		})
	}
	if err := credentialServerAuth(bufio.NewReader(strings.NewReader("bad")), io.Discard, 1234, guid); err == nil {
		t.Fatal("accepted authentication without local credential marker")
	}
}
