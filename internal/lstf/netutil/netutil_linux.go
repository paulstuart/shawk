// +build linux

package netutil

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/elastic/gosigar/sys/linux"
	gnet "github.com/shirou/gopsutil/net"
	"golang.org/x/xerrors"
)

// NetlinkError represents netlink error.
type NetlinkError struct {
	msg string
}

func (e *NetlinkError) Error() string {
	return fmt.Sprintf("Netlink error: %s", e.msg)
}

// NetlinkConnections returns connection stats.
func NetlinkConnections() ([]*linux.InetDiagMsg, error) {
	req := linux.NewInetDiagReq()
	msgs, err := linux.NetlinkInetDiag(req)
	if err != nil {
		return nil, xerrors.Errorf("NetlinkInetDiag: %w", &NetlinkError{})
	}
	return msgs, nil
}

type UserEntByLport map[string]*UserEnt

// NetlinkFilterByLocalListeningPorts filters ConnectionStat slice by the local listening ports.
func NetlinkFilterByLocalListeningPorts(conns []*linux.InetDiagMsg) ([]*linux.InetDiagMsg, error) {
	lconns := []*linux.InetDiagMsg{}
	for _, conn := range conns {
		if linux.TCPState(conn.State) != linux.TCP_LISTEN {
			continue
		}
		sip := conn.SrcIP().String()
		if sip == "0.0.0.0" || sip == "127.0.0.1" || sip == "::" {
			lconns = append(lconns, conn)
		}
	}
	return lconns, nil
}

// NetlinkLodalListeningPorts returns the local listening ports.
func NetlinkLocalListeningPorts() ([]string, error) {
	msgs, err := NetlinkConnections()
	if err != nil {
		return nil, err
	}
	ports := make([]string, 0, len(msgs))
	for _, diag := range msgs {
		if linux.TCPState(diag.State) != linux.TCP_LISTEN {
			continue
		}
		ports = append(ports, fmt.Sprintf("%v", diag.SrcPort()))
	}
	return ports, nil
}

const (
	tcpProcFilename = "/proc/net/tcp"
)

// Addr is <addr>:<port>.
type Addr struct {
	IP   string `json:"ip"`
	Port uint32 `json:"port"`
}

// ConnectionStat represents staticstics for a connection.
type ConnectionStat struct {
	Laddr  Addr
	Raddr  Addr
	Status linux.TCPState
}

// ProcfsConnections returns connection stats.
// ref. https://github.com/shirou/gopsutil/blob/c23bcca55e77b8389d84b09db8c5ac2b472070ef/net/net_linux.go#L656
func ProcfsConnections() ([]*ConnectionStat, error) {
	body, err := ioutil.ReadFile(tcpProcFilename)
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(body, []byte("\n"))
	conns := make([]*ConnectionStat, 0, len(lines)-1)
	for _, line := range lines[1:] {
		l := strings.Fields(string(line))
		if len(l) < 10 {
			continue
		}
		laddr := l[1]
		raddr := l[2]
		status, err := strconv.ParseUint(l[3], 16, 8)
		if err != nil {
			log.Printf("decode error: %v", err)
		}
		la, err := decodeAddress(laddr)
		if err != nil {
			continue
		}
		ra, err := decodeAddress(raddr)
		if err != nil {
			continue
		}

		conns = append(conns, &ConnectionStat{
			Laddr:  la,
			Raddr:  ra,
			Status: linux.TCPState(status),
		})
	}

	return conns, nil
}

// decodeAddress decode addresse represents addr in proc/net/*
// ex:
// "0500000A:0016" -> "10.0.0.5", 22
// "0085002452100113070057A13F025401:0035" -> "2400:8500:1301:1052:a157:7:154:23f", 53
// ref. https://github.com/shirou/gopsutil/blob/c23bcca55e77b8389d84b09db8c5ac2b472070ef/net/net_linux.go#L600
func decodeAddress(src string) (Addr, error) {
	t := strings.Split(src, ":")
	if len(t) != 2 {
		return Addr{}, xerrors.Errorf("does not contain port, %s", src)
	}
	addr := t[0]
	port, err := strconv.ParseInt("0x"+t[1], 0, 64)
	if err != nil {
		return Addr{}, xerrors.Errorf("invalid port, %s", src)
	}
	decoded, err := hex.DecodeString(addr)
	if err != nil {
		return Addr{}, xerrors.Errorf("decode error, %s", err)
	}
	var ip net.IP
	// Assumes this is little_endian
	ip = net.IP(gnet.Reverse(decoded))
	return Addr{
		IP:   ip.String(),
		Port: uint32(port),
	}, nil
}

// FilterByLocalListeningPorts filters ConnectionStat slice by the local listening ports.
func FilterByLocalListeningPorts(conns []*ConnectionStat) ([]string, error) {
	ports := []string{}
	for _, conn := range conns {
		if conn.Status != linux.TCP_LISTEN {
			continue
		}
		if conn.Laddr.IP == "0.0.0.0" || conn.Laddr.IP == "127.0.0.1" || conn.Laddr.IP == "::" {
			ports = append(ports, fmt.Sprintf("%d", conn.Laddr.Port))
		}
	}
	return ports, nil
}

// LocalListeningPorts returns the local listening ports.
func LocalListeningPorts() ([]string, error) {
	conns, err := ProcfsConnections()
	if err != nil {
		return nil, err
	}
	return FilterByLocalListeningPorts(conns)
}

type procStat struct {
	Pname string // process name
	Ppid  int    // parent process id
	Pgrp  int    // process group id
}

func parseProcStat(root string, pid int) (*procStat, error) {
	stat := fmt.Sprintf("%s/%d/stat", root, pid)
	f, err := os.Open(stat)
	if err != nil {
		return nil, xerrors.Errorf("could not open %s: %w", stat, err)
	}
	defer f.Close()

	var (
		pid2  int
		comm  string
		state string
		ppid  int
		pgrp  int
	)
	if _, err := fmt.Fscan(f, &pid2, &comm, &state, &ppid, &pgrp); err != nil {
		return nil, xerrors.Errorf("could not scan '%s': %w", stat, err)
	}

	var pname string
	// workaround: Sscanf return io.ErrUnexpectedEOF without knowing why.
	if _, err := fmt.Sscanf(comm, "(%s)", &pname); err != nil && err != io.ErrUnexpectedEOF {
		return nil, xerrors.Errorf("could not scan '%s': %w", comm, err)
	}

	return &procStat{
		Pname: strings.TrimRight(pname, ")"),
		Ppid:  ppid,
		Pgrp:  pgrp,
	}, nil
}

const socketPrefix = "socket:["

func parseSocketInode(lnk string) (uint32, error) {
	ind := strings.Index(lnk, socketPrefix)
	if ind == -1 {
		return 0, nil
	}
	var ino uint32
	n, err := fmt.Sscanf(lnk, "socket:[%d]", &ino)
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, xerrors.Errorf("'%s' should be pattern '[socket:\\%d]'", lnk)
	}
	return ino, nil
}

// BuildUserEntries scans under /proc/%pid/fd/.
func BuildUserEntries() (UserEnts, error) {
	root := os.Getenv("PROC_ROOT")
	if root == "" {
		root = "/proc"
	}

	dir, err := ioutil.ReadDir(root)
	if err != nil {
		return nil, err
	}

	userEnts := make(UserEnts, 0)

	for _, d := range dir {
		// find only "<pid>"" directory
		if !d.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}

		// skip self process
		if pid == os.Getpid() {
			continue
		}

		pidDir := filepath.Join(root, d.Name())
		fdDir := filepath.Join(pidDir, "fd")

		// exists fd?
		fi, err := os.Stat(fdDir)
		switch {
		case err != nil:
			return nil, xerrors.Errorf("stat %s: %v", fdDir, err)
		case !fi.IsDir():
			continue
		}

		dir2, err := ioutil.ReadDir(fdDir)
		if err != nil {
			pathErr := err.(*os.PathError)
			errno := pathErr.Err.(syscall.Errno)
			if errno == syscall.EACCES {
				// ignore "open: <path> permission denied"
				continue
			}
			return nil, xerrors.Errorf("readdir %s: %v", errno, err)
		}

		var stat *procStat

		for _, d2 := range dir2 {
			fd, err := strconv.Atoi(d2.Name())
			if err != nil {
				continue
			}
			fdpath := filepath.Join(fdDir, d2.Name())
			lnk, err := os.Readlink(fdpath)
			if err != nil {
				pathErr := err.(*os.PathError)
				errno := pathErr.Err.(syscall.Errno)
				if errno == syscall.ENOENT {
					// ignore "readlink: no such file or directory"
					// because fdpath is disappear depending on timing
					continue
				}
				return nil, xerrors.Errorf("readlink %s: %v", fdpath, err)
			}
			ino, err := parseSocketInode(lnk)
			if err != nil {
				return nil, err
			}
			if ino == 0 {
				continue
			}

			if stat == nil {
				stat, err = parseProcStat(root, pid)
				if err != nil {
					return nil, err
				}
			}

			userEnts[ino] = &UserEnt{
				inode: ino,
				fd:    fd,
				pid:   pid,
				pname: stat.Pname,
				ppid:  stat.Ppid,
				pgrp:  stat.Pgrp,
			}
		}
	}
	return userEnts, nil
}
