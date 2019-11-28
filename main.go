package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	//disable security check in global
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
}

type Response struct {
	Name         string
	IP           string
	RSS          uint64
	Load         string
	Uptime       uint64
	MemAvail     uint64
	MemTotal     uint64
	Login        int
	TCP          int
	UDP          int
	DiskRead     []uint64
	DiskWrite    []uint64
	NetRead      []uint64
	NetWrite     []uint64
	NetReadNum   []uint64
	NetWriteNum  []uint64
	CPUS         []float64
	PostUnixTime int64
	Time         time.Duration
}

func main() {
	url := flag.String("url", "", "post server url")
	flag.Parse()

	if *url == "" {
		log.Println("url is empty")
		return
	}

	for {
		st := time.Now()
		resp := &Response{
			IP:     getIp(),
			RSS:    GetProgramRss(),
			Load:   GetSystemLoadFromProc(),
			Uptime: GetUptime(),
			TCP:    lineCounterWrap(os.Open("/proc/net/tcp")) + lineCounterWrap(os.Open("/proc/net/tcp6")) - 2,
			UDP:    lineCounterWrap(os.Open("/proc/net/udp")) + lineCounterWrap(os.Open("/proc/net/udp6")) - 2,
			CPUS:   GetCPUUsageSlice(),
			Login:  LoginUsers(),
		}
		resp.Name, _ = os.Hostname()
		resp.MemAvail, resp.MemTotal = GetMemInfoFromProc()
		resp.DiskRead, resp.DiskWrite = GetDiskStatSlice()
		resp.NetRead, resp.NetWrite, resp.NetReadNum, resp.NetWriteNum = GetNetTrafficSlice()

		resp.PostUnixTime = time.Now().Unix()
		resp.Time = time.Since(st)
		s, _ := json.Marshal(resp)
		(&http.Client{Timeout: time.Minute}).Post(*url, "Content-Type: application/json; charset=utf-8", bytes.NewBuffer(s))
		time.Sleep(time.Minute)
	}
}

var taskListDoOnceInDuration = NewDoOnceInDuration(time.Second*9 + (323 * time.Millisecond))
var getProgramRssWindowsCache uint64
var windowsFindRssRex = regexp.MustCompile("(?i)([\\d,]+)\\s?K$")
var intRex = regexp.MustCompile("[0-9]+")
var onlyWhiteSpaceRex = regexp.MustCompile(`[ ]+`)

const BlockSize = 512

var ip string

var getIpOnceDuration = NewDoOnceInDuration(time.Hour * 2)

func getIp() string {
	getIpOnceDuration.Do(func() {
		resp, err := http.Get("https://ip.flysay.com")
		if err != nil {
			return
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return
		}
		s := strings.TrimSpace(string(body))
		if len(s) > 4 {
			ip = s[0:len(s)/2] + "****"
		}
	})
	return ip
}

var getLoginUsersOnceDuration = NewDoOnceInDuration(time.Minute)
var LoginUsersOnce int

func LoginUsers() int {
	getLoginUsersOnceDuration.Do(func() {
		//slow
		if out, err := exec.Command("who").Output(); err == nil {
			LoginUsersOnce = len(strings.Split(string(out), "\n"))
		}
	})
	return LoginUsersOnce
}

var CPUTotalSlice = make([]uint64, 15)
var CPUIdleSlice = make([]uint64, 15)

var getCPUUsageSliceDoOnce sync.Once

func GetCPUUsageSlice() (usages []float64) {
	getCPUUsageSliceDoOnce.Do(func() {
		const second = 60
		go func() {
			time.Sleep(time.Second * 2)
			prevTotal, prevIdle := cpuTime()
			for {
				time.Sleep(time.Second * second)
				total, idle := cpuTime()
				CPUTotalSlice = append(CPUTotalSlice[1:], (total-prevTotal)/second)
				CPUIdleSlice = append(CPUIdleSlice[1:], (idle-prevIdle)/second)
				prevTotal, prevIdle = total, idle
			}
		}()
	})

	for i, total := range CPUTotalSlice {
		if total == 0 {
			usages = append(usages, 0)
		} else {
			usages = append(usages, math.Round(float64(total-CPUIdleSlice[i])/float64(total)*10000)/100)
		}
	}
	return
}

func cpuTime() (total, idle uint64) {
	if dat, err := ioutil.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(onlyWhiteSpaceRex.ReplaceAllString(string(dat), " "), "\n") {
			sp := strings.Split(line, " ")
			if !strings.HasPrefix(sp[0], "cpu") {
				break
			}
			if sp[0] == "cpu" {
				continue
			}

			user, _ := strconv.ParseUint(sp[1], 10, 64)
			nice, _ := strconv.ParseUint(sp[2], 10, 64)
			system, _ := strconv.ParseUint(sp[3], 10, 64)
			idle, _ := strconv.ParseUint(sp[4], 10, 64)
			iowait, _ := strconv.ParseUint(sp[5], 10, 64)
			irq, _ := strconv.ParseUint(sp[6], 10, 64)
			softirq, _ := strconv.ParseUint(sp[7], 10, 64)
			stealstolen, _ := strconv.ParseUint(sp[8], 10, 64)
			guest, _ := strconv.ParseUint(sp[9], 10, 64)
			return user + nice + system + idle + iowait + irq + softirq + stealstolen + guest, idle
		}
	}

	return
}

func lineCounterWrap(r *os.File, err error) (count int) {
	defer r.Close()

	if err != nil {
		return
	}
	buf := make([]byte, 32*1024)
	lineSep := []byte{'\n'}

	for {
		c, err := r.Read(buf)
		count += bytes.Count(buf[:c], lineSep)

		switch {
		case err == io.EOF:
			return count
		case err != nil:
			return count
		}
	}
}

func GetMemInfoFromProc() (available, total uint64) {
	if runtime.GOOS == "windows" {
		return
	}

	// system mem info
	if dat, err := ioutil.ReadFile("/proc/meminfo"); err == nil {
		for index, line := range strings.SplitN(string(dat), "\n", 6) {

			if strings.Contains(strings.ToLower(line), "memtotal") {
				if res := intRex.FindAllString(line, 1); len(res) == 1 {
					if kb, err := strconv.ParseUint(res[0], 10, 64); err == nil {
						total = kb * 1024
					}
				}
			}
			if strings.Contains(strings.ToLower(line), "memavailable") {
				if res := intRex.FindAllString(line, 1); len(res) == 1 {
					if kb, err := strconv.ParseUint(res[0], 10, 64); err == nil {
						available = kb * 1024
					}
				}
			}
			if index > 4 {
				break
			}
		}
	}

	return
}

type DiskStat struct {
	Dev   string
	Read  uint64
	Write uint64
}

var ReadSlice = make([]uint64, 15)
var WriteSlice = make([]uint64, 15)

var getDiskStatSliceDoOnce sync.Once

func GetDiskStatSlice() ([]uint64, []uint64) {
	getDiskStatSliceDoOnce.Do(func() {
		const second = 60
		go func() {
			time.Sleep(time.Second * 2)
			var prevR uint64
			var prevW uint64
			for _, e := range GetDiskStat() {
				prevR += e.Read
				prevW += e.Write
			}
			for {
				time.Sleep(time.Second * second)
				var r uint64
				var w uint64
				for _, e := range GetDiskStat() {
					r += e.Read
					w += e.Write
				}
				ReadSlice = append(ReadSlice[1:], (r-prevR)/second)
				WriteSlice = append(WriteSlice[1:], (w-prevW)/second)
				prevR, prevW = r, w
			}
		}()
	})
	return ReadSlice, WriteSlice
}

func GetDiskStat() (d []*DiskStat) {
	matches, _ := filepath.Glob("/sys/block/*/stat")
	for _, match := range matches {
		data, err := ioutil.ReadFile(match)
		if err != nil {
			continue
		}
		sp := strings.Split(strings.TrimSpace(onlyWhiteSpaceRex.ReplaceAllString(string(data), " ")), " ")
		if len(sp) < 7 {
			continue
		}
		r, _ := strconv.ParseUint(sp[2], 10, 64)
		w, _ := strconv.ParseUint(sp[6], 10, 64)
		if r == 0 && w == 0 {
			continue
		}
		d = append(d, &DiskStat{
			Dev:   strings.Split(match, "/")[3],
			Read:  r * BlockSize,
			Write: w * BlockSize,
		})
	}
	return
}

var RxSlice = make([]uint64, 15)
var TxSlice = make([]uint64, 15)
var RpSlice = make([]uint64, 15)
var TpSlice = make([]uint64, 15)
var getNetTrafficSliceDoOnce sync.Once

func GetNetTrafficSlice() ([]uint64, []uint64, []uint64, []uint64) {
	//net traffic counter
	getNetTrafficSliceDoOnce.Do(func() {
		//if runtime.GOOS == "linux" {
		const second = 60
		go func() {
			time.Sleep(time.Second * 2)
			prevRx, prevTx, prevRp, prevTp := GetNetTraffic()
			for {
				time.Sleep(time.Second * second)
				rx, tx, rp, tp := GetNetTraffic()
				RxSlice = append(RxSlice[1:], (rx-prevRx)/second)
				TxSlice = append(TxSlice[1:], (tx-prevTx)/second)
				RpSlice = append(RpSlice[1:], (rp-prevRp)/second)
				TpSlice = append(TpSlice[1:], (tp-prevTp)/second)
				prevRx, prevTx, prevRp, prevTp = rx, tx, rp, tp
			}
		}()
		//}
	})

	return RxSlice, TxSlice, RpSlice, TpSlice
}

func GetNetTraffic() (rx, tx, rp, tp uint64) {
	if runtime.GOOS != "linux" {
		return
	}

	if dat, err := ioutil.ReadFile("/proc/net/dev"); err == nil {
		out := strings.ToLower(onlyWhiteSpaceRex.ReplaceAllString(string(dat), " "))
		lines := strings.Split(out, "\n")
		if len(lines) < 3 {
			return
		}
		lines = lines[2:]
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "lo:") {
				continue
			}
			nodes := strings.SplitN(line, " ", 12)
			if len(nodes) < 12 {
				return
			}

			//bytes
			r, _ := strconv.ParseUint(nodes[1], 10, 64)
			rx += r
			t, _ := strconv.ParseUint(nodes[9], 10, 64)
			tx += t

			//packets
			p1, _ := strconv.ParseUint(nodes[2], 10, 64)
			rp += p1
			p2, _ := strconv.ParseUint(nodes[10], 10, 64)
			tp += p2
		}
	}
	return
}

func GetUptime() (sec uint64) {
	if dat, err := ioutil.ReadFile("/proc/uptime"); err == nil {
		sp := strings.SplitN(string(dat), " ", 2)
		f, _ := strconv.ParseFloat(sp[0], 64)
		sec = uint64(f)
	}
	return
}

func GetSystemLoadFromProc() (loadStr string) {
	if runtime.GOOS == "windows" {
		return
	}

	if dat, err := ioutil.ReadFile("/proc/loadavg"); err == nil {
		for index, str := range strings.SplitN(string(dat), " ", 4) {
			loadStr += str + " "
			if index == 2 {
				break
			}
		}
	}
	strings.TrimSpace(loadStr)
	return
}

type DoOnceInDuration struct {
	duration time.Duration
	once     *sync.Once
}

func NewDoOnceInDuration(duration time.Duration) *DoOnceInDuration {
	return &DoOnceInDuration{duration: duration, once: new(sync.Once)}
}
func (my *DoOnceInDuration) Do(f func()) (isRun bool) {
	my.once.Do(func() {
		f()
		isRun = true
		go func() {
			time.Sleep(my.duration)
			my.once = new(sync.Once)
		}()
	})

	return
}

func GetProgramRss() (rss uint64) {
	if runtime.GOOS == "windows" {
		taskListDoOnceInDuration.Do(func() {
			go func() {
				if out, err := exec.Command("tasklist", "/fi", "pid  eq "+strconv.Itoa(os.Getpid()), "/FO", "LIST").Output(); err == nil { //slow
					for _, line := range strings.Split(string(out), "\n") {
						if res := windowsFindRssRex.FindStringSubmatch(strings.TrimSpace(line)); len(res) == 2 {
							if kb, err := strconv.ParseFloat(strings.ReplaceAll(res[1], ",", ""), 64); err == nil {
								getProgramRssWindowsCache = uint64(kb) * 1024
								return
							}
						}
					}
				}
			}()
		})

		return getProgramRssWindowsCache
	}

	// program mem info
	if dat, err := ioutil.ReadFile("/proc/" + strconv.Itoa(os.Getpid()) + "/status"); err == nil {
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.Contains(strings.ToLower(line), "vmrss") {
				if res := intRex.FindAllString(line, 1); len(res) == 1 {
					if kb, err := strconv.ParseUint(res[0], 10, 64); err == nil {
						rss = kb * 1024
					}
				}
				break
			}
		}
	}
	return
}
