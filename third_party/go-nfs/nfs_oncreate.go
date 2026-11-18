package nfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
)

const (
	createModeUnchecked = 0
	createModeGuarded   = 1
	createModeExclusive = 2
)

func onCreate(ctx context.Context, w *response, userHandle Handler) error {
	w.errorFmt = wccDataErrorFormatter
	obj := DirOpArg{}
	err := xdr.Read(w.req.Body, &obj)
	if err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}
	how, err := xdr.ReadUint32(w.req.Body)
	if err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}
	var attrs *SetFileAttributes
	var verf [8]byte
	if how == createModeUnchecked || how == createModeGuarded {
		sattr, err := ReadSetFileAttributes(w.req.Body)
		if err != nil {
			return &NFSStatusError{NFSStatusInval, err}
		}
		attrs = sattr
	} else if how == createModeExclusive {
		if err := xdr.Read(w.req.Body, &verf); err != nil {
			return &NFSStatusError{NFSStatusInval, err}
		}
	} else {
		// invalid
		return &NFSStatusError{NFSStatusNotSupp, os.ErrInvalid}
	}

	fs, path, err := userHandle.FromHandle(obj.Handle)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}
	if !billy.CapabilityCheck(fs, billy.WriteCapability) {
		return &NFSStatusError{NFSStatusROFS, os.ErrPermission}
	}

	if len(string(obj.Filename)) > PathNameMax {
		return &NFSStatusError{NFSStatusNameTooLong, nil}
	}

	newFile := append(path, string(obj.Filename))
	newFilePath := fs.Join(newFile...)
	changer := userHandle.Change(fs)
	created := true
	if s, err := fs.Stat(newFilePath); err == nil {
		if s.IsDir() {
			return &NFSStatusError{NFSStatusExist, nil}
		}
		if how == createModeGuarded {
			return &NFSStatusError{NFSStatusExist, os.ErrPermission}
		}
		if how == createModeExclusive {
			// A retransmitted exclusive create finds the verifier it stored.
			if !s.ModTime().Equal(exclusiveTime(verf[:4])) {
				return &NFSStatusError{NFSStatusExist, os.ErrExist}
			}
			created = false
		}
	} else {
		if s, err := fs.Stat(fs.Join(path...)); err != nil {
			return &NFSStatusError{NFSStatusAccess, err}
		} else if !s.IsDir() {
			return &NFSStatusError{NFSStatusNotDir, nil}
		}
	}

	if created {
		file, err := fs.Create(newFilePath)
		if err != nil {
			Log.Errorf("Error Creating: %v", err)
			return &NFSStatusError{NFSStatusAccess, err}
		}
		if err := file.Close(); err != nil {
			Log.Errorf("Error Creating: %v", err)
			return &NFSStatusError{NFSStatusAccess, err}
		}
	}

	fp := userHandle.ToHandle(fs, newFile)
	if how == createModeExclusive {
		// The verifier lives in the file times until the client's SETATTR replaces them (RFC 1813, CREATE3).
		if changer == nil {
			return &NFSStatusError{NFSStatusNotSupp, os.ErrPermission}
		}
		if err := changer.Chtimes(newFilePath, exclusiveTime(verf[4:]), exclusiveTime(verf[:4])); err != nil {
			return &NFSStatusError{NFSStatusIO, err}
		}
	} else if err := attrs.Apply(changer, fs, newFilePath); err != nil {
		Log.Errorf("Error applying attributes: %v\n", err)
		return &NFSStatusError{NFSStatusIO, err}
	}

	writer := bytes.NewBuffer([]byte{})
	if err := xdr.Write(writer, uint32(NFSStatusOk)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	// "handle follows"
	if err := xdr.Write(writer, uint32(1)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := xdr.Write(writer, fp); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := WritePostOpAttrs(writer, tryStat(fs, newFile)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	// dir_wcc (we don't include pre_op_attr)
	if err := xdr.Write(writer, uint32(0)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := WritePostOpAttrs(writer, tryStat(fs, path)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	if err := w.Write(writer.Bytes()); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	return nil
}

func exclusiveTime(half []byte) time.Time {
	return time.Unix(int64(binary.BigEndian.Uint32(half)), 0)
}
