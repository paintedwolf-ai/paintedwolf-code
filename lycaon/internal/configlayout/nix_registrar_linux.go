package configlayout

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	nixPhysicalStore = "/nix/store"
	nixPhysicalState = "/nix/var/nix"
	nixDaemonSocket  = nixPhysicalState + "/daemon-socket/socket"
)

type nixRegistrar struct{ storeURI string }

type nixStorageFact struct {
	kind uint32
	mode uint32
	uid  uint32
}

func nixStorageStat(path string) nixStorageFact {
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil {
		return nixStorageFact{}
	}
	return nixStorageFact{kind: st.Mode & unix.S_IFMT, mode: st.Mode, uid: st.Uid}
}

func runningNixRegistrar(layout *NixLayout) (nixRegistrar, error) {
	if filepath.Dir(layout.Output) != nixPhysicalStore {
		return nixRegistrar{}, errors.New("Nix retention requires an explicitly supported physical store")
	}
	uid := uint32(os.Getuid())
	for _, parent := range []string{"/nix", "/nix/var"} {
		st := nixStorageStat(parent)
		if !st.directory(uid, false) {
			return nixRegistrar{}, errors.New("Nix retention storage parents are unavailable or unsafe")
		}
	}
	return selectNixRegistrar(uid, nixStorageStat(nixPhysicalStore), nixStorageStat(nixPhysicalState),
		nixStorageStat(filepath.Dir(nixDaemonSocket)), nixStorageStat(nixDaemonSocket))
}

func (st nixStorageFact) directory(uid uint32, sticky bool) bool {
	return st.kind == unix.S_IFDIR && (st.uid == 0 || st.uid == uid) &&
		(st.mode&0o022 == 0 || sticky && st.mode&0o7777 == 0o1775)
}

func selectNixRegistrar(uid uint32, store, state, socketDirectory, socket nixStorageFact) (nixRegistrar, error) {
	if !store.directory(uid, true) || !state.directory(uid, false) {
		return nixRegistrar{}, errors.New("Nix retention storage is unavailable or unsafe")
	}
	settings := url.Values{"store": {nixPhysicalStore}, "real": {nixPhysicalStore}, "state": {nixPhysicalState}}
	if store.uid == uid && state.uid == uid {
		return nixRegistrar{storeURI: "local?" + settings.Encode()}, nil
	}
	if store.uid == 0 && state.uid == 0 && socketDirectory.uid == 0 &&
		socketDirectory.directory(0, false) && socket.uid == 0 && socket.kind == unix.S_IFSOCK {
		return nixRegistrar{storeURI: "unix://" + nixDaemonSocket + "?" + settings.Encode()}, nil
	}
	return nixRegistrar{}, errors.New("The running Nix store has no supported local GC authority")
}

// No caller configuration, plugins, remote store, substitution or builders take
// part in registering an already installed path with its physical GC authority.
func (r nixRegistrar) environment(config, cache string) []string {
	return []string{
		"HOME=" + config, "XDG_CONFIG_HOME=" + config, "XDG_CONFIG_DIRS=" + config,
		"NIX_CONF_DIR=" + config, "NIX_USER_CONF_FILES=/dev/null",
		"XDG_CACHE_HOME=" + cache, "NIX_CACHE_HOME=" + cache,
		"XDG_STATE_HOME=" + cache, "NIX_STATE_HOME=" + cache,
		"PATH=/run/current-system/sw/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C",
		"NIX_CONFIG=substitute = false\nsubstituters =\nbuilders =\nmax-jobs = 0\nplugin-files =\n",
	}
}
