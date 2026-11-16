//go:build !windows

package aiengine

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// TotalRAM 物理内存总量（字节）；失败时返回 0。
func TotalRAM() uint64 {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			n, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
			return n
		}
		return 0
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 2 && fs[0] == "MemTotal:" {
			n, _ := strconv.ParseUint(fs[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}

func checkFreeSpace(dir string, need int64) error {
	if err := mkdirAll(dir); err != nil {
		return err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return nil
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < need {
		return fmt.Errorf("磁盘空间不足：需要约 %.1f GB，只剩 %.1f GB", float64(need)/(1<<30), float64(free)/(1<<30))
	}
	return nil
}
