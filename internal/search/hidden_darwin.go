//go:build darwin

package search

import (
	"io/fs"
	"strings"
	"syscall"
)

// ufHidden 对应 <sys/stat.h> 中的 UF_HIDDEN（Finder 中隐藏，如 ~/Library）。
const ufHidden = 0x8000

func isHiddenOrSystem(info fs.FileInfo) bool {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Flags&ufHidden != 0
	}
	return false
}

// macOS 的“包”（.app 等）在访达中显示为单个文件：可以按名称搜到，但不进入其内部。
var packageExts = map[string]bool{
	".app": true, ".framework": true, ".bundle": true, ".plugin": true, ".kext": true, ".appex": true,
	".photoslibrary": true, ".musiclibrary": true, ".tvlibrary": true, ".photolibrary": true,
	".xcarchive": true, ".xcodeproj": true, ".xcworkspace": true, ".playground": true,
	".rtfd": true, ".pages": true, ".numbers": true, ".key": true, ".band": true, ".logicx": true,
	".fcpbundle": true, ".imovielibrary": true, ".sparsebundle": true, ".pkg": true, ".mpkg": true,
}

func isPackageDir(name string) bool {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return false
	}
	return packageExts[strings.ToLower(name[i:])]
}
