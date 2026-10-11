package projectsource

import (
	"encoding/binary"
	"os"

	"golang.org/x/sys/windows"
)

func sourceChangeTime(file *os.File, _ os.FileInfo) ([2]int64, error) {
	var basic [40]byte
	if err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileBasicInfo, &basic[0], uint32(len(basic))); err != nil {
		return [2]int64{}, err
	}
	return [2]int64{int64(binary.LittleEndian.Uint64(basic[24:32])), 0}, nil
}
