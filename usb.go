package usb

import (
	"fmt"
	"log"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

type Transfer struct {
	Status int32          // transaction status (0 == success)
	Length int32          // length of data transferred
	Data   []byte         // data to transmit or receive
	Done   chan *Transfer // written to on completion
	urb    usbdevfs_urb
}

type Device struct {
	fd     int
	lock   sync.Mutex
	active map[uintptr]*Transfer
	log    *log.Logger
}

// This ioctl is interruptible by signals and will not wedge the process on
// exit, but otherwise blocks forever if no URBs complete.  Probably should
// poll/select and use the nonblocking version to allow for better cleanup
// on Close()
func (u *Device) reaper() {
	var x int
	var n uint32
	for x = 0; x < 4; x++ {
		n = uint32(x)
		_, _, e := ioctl(u.fd, USBDEVFS_REAPURB, uintptr(unsafe.Pointer(&n)))
		if e != nil {
			fmt.Println("failure reaping URBs", e)
			break
		}
		u.lock.Lock()
		xfer := u.active[uintptr(unsafe.Pointer(&n))]
		delete(u.active, uintptr(unsafe.Pointer(&n)))
		u.lock.Unlock()
		if xfer == nil {
			fmt.Println("kernel returned invalid urb pointer?!")
			continue
		}
		xfer.Status = xfer.urb.status
		xfer.Length = xfer.urb.actual_length
		fmt.Println("status ", xfer.urb.status)
		fmt.Println("actual ", xfer.urb.actual_length)
		if xfer.Done != nil {
			xfer.Done <- xfer
		}
	}
}

func OpenVidPid(vid uint16, pid uint16) (*Device, error) {
	for di := DeviceInfoList(); di != nil; di = di.Next {
		if (vid != di.VendorID) || (pid != di.ProductID) {
			continue
		}
		return Open(di)
	}
	return nil, syscall.ENODEV
}

func OpenBusDev(bus int, dev int) (*Device, error) {
	for di := DeviceInfoList(); di != nil; di = di.Next {
		if (bus != di.BusNum) || (dev != di.DevNum) {
			continue
		}
		return Open(di)
	}
	return nil, syscall.ENODEV
}

func Open(di *DeviceInfo) (*Device, error) {
	fd, e := syscall.Open(di.devpath, os.O_RDWR|syscall.O_CLOEXEC, 0666)
	if e != nil {
		return nil, e
	}
	dev := &Device{
		fd:     fd,
		active: make(map[uintptr]*Transfer),
		log:    log.New(os.Stderr, "usb: ", 0),
	}
	//dev.reaper()
	return dev, nil
}

func (u *Device) Close() {
	// TODO: sanely shutdown reaper
	u.lock.Lock()
	syscall.Close(u.fd)
	u.fd = -1
	u.lock.Unlock()
}

func (u *Device) ClaimInterface(n uint32) error {
	_, _, e := ioctl(u.fd, USBDEVFS_CLAIMINTERFACE, uintptr(unsafe.Pointer(&n)))
	return e
}

func (u *Device) ReleaseInterface(n uint32) error {
	_, _, e := ioctl(u.fd, USBDEVFS_RELEASEINTERFACE, uintptr(unsafe.Pointer(&n)))
	return e
}

func (u *Device) ClearHalt(endpoint uint8) error {
	var n = uint32(endpoint)
	_, _, e := ioctl(u.fd, USBDEVFS_CLEAR_HALT, uintptr(unsafe.Pointer(&n)))
	return e
}

func (u *Device) SetConfiguration(num uint8) error {
	var n = uint32(num)
	_, _, e := ioctl(u.fd, USBDEVFS_SETCONFIGURATION, uintptr(unsafe.Pointer(&n)))
	return e
}

func (u *Device) SetInterface(num uint8, alt uint8) error {
	x := usbdevfs_setifc{uint32(num), uint32(alt)}
	_, _, e := ioctl(u.fd, USBDEVFS_SETINTERFACE, uintptr(unsafe.Pointer(&x)))
	return e
}

func (u *Device) DisconnectDriver(ifc uint8) error {
	x := usbdevfs_ioctl{uint32(ifc), USBDEVFS_DISCONNECT, 0}
	_, _, e := ioctl(u.fd, USBDEVFS_IOCTL, uintptr(unsafe.Pointer(&x)))
	return e
}

func (u *Device) ControlTransfer(
	reqtype uint8, request uint8, value uint16, index uint16,
	length uint16, timeout uint32, data []byte) (int, error) {

	if int(length) > len(data) {
		return 0, syscall.ENOSPC
	}
	p := unsafe.Pointer(&data[0])
	ct := ctrltransfer{reqtype, request, value, index, length, timeout, 0, uintptr(p)}
	n, _, e := ioctl(u.fd, USBDEVFS_CONTROL, uintptr(unsafe.Pointer(&ct)))
	return n, e
}

func (u *Device) BulkTransfer(endpoint uint32, length uint32, timeout uint32, inData []byte) (int, []byte, error) {

	if int(length) > len(inData) {
		return 0, nil, syscall.ENOSPC
	}
	p := unsafe.Pointer(&inData[0])
	bt := bulktransfer{endpoint, length, timeout, 0, uintptr(p)}
	n, _, e := ioctl(u.fd, USBDEVFS_BULK, uintptr(unsafe.Pointer(&bt)))
	//fmt.Printf("ioctl return n = %d\n", n)
	if e != nil {
		fmt.Println("ERROR: ioctl error")
		fmt.Println(e)
	}
	//binary.LittleEndian.PutUint64(b, uint64(r))
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		//fmt.Printf("b[%d] = %d\n", i, inData[i])
		b[i] = inData[i]
		//b[i] = *(*byte)(unsafe.Pointer(&r))
		//b[i] += 127
	}

	return n, b, e
}

func ioctl(fd int, req uintptr, arg uintptr) (int, uintptr, error) {
	r, b, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg)
	if e == 0 {
		return int(r), b, nil
	}
	return 0, b, e
}
