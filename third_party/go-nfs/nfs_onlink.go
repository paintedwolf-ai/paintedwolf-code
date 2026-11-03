package nfs

import (
	"bytes"
	"context"
	"os"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
)

// onLink handles NFSProcedureLink (RFC 1813 section 3.3.15).
func onLink(ctx context.Context, w *response, userHandle Handler) error {
	linkErrorBody := [12]byte{}
	w.errorFmt = errFormatterWithBody(linkErrorBody[:])
	targetHandle, err := xdr.ReadOpaque(w.req.Body)
	if err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}
	obj := DirOpArg{}
	if err := xdr.Read(w.req.Body, &obj); err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}

	targetFS, targetPath, err := userHandle.FromHandle(targetHandle)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}
	fs, path, err := userHandle.FromHandle(obj.Handle)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}
	if targetFS != fs {
		return &NFSStatusError{NFSStatusXDev, os.ErrInvalid}
	}
	if !billy.CapabilityCheck(fs, billy.WriteCapability) {
		return &NFSStatusError{NFSStatusROFS, os.ErrPermission}
	}

	if len(string(obj.Filename)) > PathNameMax {
		return &NFSStatusError{NFSStatusNameTooLong, os.ErrInvalid}
	}

	newFilePath := fs.Join(append(path, string(obj.Filename))...)
	if _, err := fs.Stat(newFilePath); err == nil {
		return &NFSStatusError{NFSStatusExist, os.ErrExist}
	}
	if s, err := fs.Stat(fs.Join(path...)); err != nil {
		return &NFSStatusError{NFSStatusAccess, err}
	} else if !s.IsDir() {
		return &NFSStatusError{NFSStatusNotDir, nil}
	}

	changer := userHandle.Change(fs)
	if changer == nil {
		return &NFSStatusError{NFSStatusAccess, os.ErrPermission}
	}
	cos, ok := changer.(UnixChange)
	if !ok {
		return &NFSStatusError{NFSStatusNotSupp, os.ErrPermission}
	}

	err = cos.Link(fs.Join(targetPath...), newFilePath)
	if err != nil {
		return &NFSStatusError{NFSStatusAccess, err}
	}

	writer := bytes.NewBuffer([]byte{})
	if err := xdr.Write(writer, uint32(NFSStatusOk)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := WritePostOpAttrs(writer, tryStat(fs, targetPath)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := WriteWcc(writer, nil, tryStat(fs, path)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	if err := w.Write(writer.Bytes()); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	return nil
}
