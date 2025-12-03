//go:build linux

package main

/*
#cgo CFLAGS: -O2
#cgo linux CFLAGS: -D_GNU_SOURCE
#include <linux/videodev2.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/select.h>
#include <sys/time.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include <string.h>
#include <stdint.h>
#include <stdlib.h>

#define MAX_BUFFERS 8

struct buffer { void* start; size_t length; };
static struct buffer buffers[MAX_BUFFERS];
static unsigned int n_buffers = 0;

int uvc_open(const char* devpath) {
    int fd = open(devpath, O_RDWR | O_NONBLOCK, 0);
    return fd;
}

int uvc_close(int fd) {
    return close(fd);
}

int uvc_querycap(int fd, struct v4l2_capability* cap) {
    if (ioctl(fd, VIDIOC_QUERYCAP, cap) == -1) {
        return -1;
    }
    return 0;
}

int uvc_set_format(int fd, uint32_t width, uint32_t height, uint32_t pixfmt, uint32_t* out_sizeimage) {
    struct v4l2_format fmt;
    memset(&fmt, 0, sizeof(fmt));
    fmt.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    fmt.fmt.pix.width = width;
    fmt.fmt.pix.height = height;
    fmt.fmt.pix.pixelformat = pixfmt;
    fmt.fmt.pix.field = V4L2_FIELD_ANY;
    if (ioctl(fd, VIDIOC_S_FMT, &fmt) == -1) {
        return -1;
    }
    if (out_sizeimage) {
        *out_sizeimage = fmt.fmt.pix.sizeimage;
    }
    return 0;
}

int uvc_get_format(int fd, uint32_t* out_w, uint32_t* out_h, uint32_t* out_pixfmt, uint32_t* out_sizeimage) {
    struct v4l2_format fmt;
    memset(&fmt, 0, sizeof(fmt));
    fmt.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    if (ioctl(fd, VIDIOC_G_FMT, &fmt) == -1) {
        return -1;
    }
    if (out_w) *out_w = fmt.fmt.pix.width;
    if (out_h) *out_h = fmt.fmt.pix.height;
    if (out_pixfmt) *out_pixfmt = fmt.fmt.pix.pixelformat;
    if (out_sizeimage) *out_sizeimage = fmt.fmt.pix.sizeimage;
    return 0;
}

int uvc_set_fps(int fd, uint32_t fps) {
    struct v4l2_streamparm parm;
    memset(&parm, 0, sizeof(parm));
    parm.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    parm.parm.capture.capability = 0;
    parm.parm.capture.capturemode = 0;
    parm.parm.capture.timeperframe.numerator = 1;
    parm.parm.capture.timeperframe.denominator = fps;
    parm.parm.capture.extendedmode = 0;
    parm.parm.capture.readbuffers = 0;
    if (ioctl(fd, VIDIOC_S_PARM, &parm) == -1) {
        return -1;
    }
    return 0;
}

int uvc_init_mmap(int fd, unsigned int count) {
    if (count > MAX_BUFFERS) count = MAX_BUFFERS;

    struct v4l2_requestbuffers req;
    memset(&req, 0, sizeof(req));
    req.count = count;
    req.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    req.memory = V4L2_MEMORY_MMAP;

    if (ioctl(fd, VIDIOC_REQBUFS, &req) == -1) {
        return -1;
    }
    if (req.count < 2) {
        errno = EINVAL;
        return -1;
    }

    unsigned int i;
    for (i = 0; i < req.count; ++i) {
        struct v4l2_buffer buf;
        memset(&buf, 0, sizeof(buf));
        buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
        buf.memory = V4L2_MEMORY_MMAP;
        buf.index = i;

        if (ioctl(fd, VIDIOC_QUERYBUF, &buf) == -1) {
            return -1;
        }

        buffers[i].length = buf.length;
        buffers[i].start = mmap(NULL, buf.length,
                                PROT_READ | PROT_WRITE, MAP_SHARED,
                                fd, buf.m.offset);

        if (buffers[i].start == MAP_FAILED) {
            return -1;
        }
    }

    n_buffers = req.count;

    for (i = 0; i < n_buffers; ++i) {
        struct v4l2_buffer buf;
        memset(&buf, 0, sizeof(buf));
        buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
        buf.memory = V4L2_MEMORY_MMAP;
        buf.index = i;
        if (ioctl(fd, VIDIOC_QBUF, &buf) == -1) {
            return -1;
        }
    }

    return 0;
}

int uvc_uninit_mmap() {
    unsigned int i;
    for (i = 0; i < n_buffers; ++i) {
        if (buffers[i].start != NULL && buffers[i].length > 0) {
            munmap(buffers[i].start, buffers[i].length);
            buffers[i].start = NULL;
            buffers[i].length = 0;
        }
    }
    n_buffers = 0;
    return 0;
}

int uvc_start_streaming(int fd) {
    enum v4l2_buf_type type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    if (ioctl(fd, VIDIOC_STREAMON, &type) == -1) {
        return -1;
    }
    return 0;
}

int uvc_stop_streaming(int fd) {
    enum v4l2_buf_type type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    if (ioctl(fd, VIDIOC_STREAMOFF, &type) == -1) {
        return -1;
    }
    return 0;
}

int uvc_dequeue2(int fd, void** out_ptr, uint32_t* out_len, uint32_t timeout_ms, uint32_t* out_index) {
    fd_set fds;
    struct timeval tv;
    int r;

    FD_ZERO(&fds);
    FD_SET(fd, &fds);

    tv.tv_sec = timeout_ms / 1000;
    tv.tv_usec = (timeout_ms % 1000) * 1000;

    r = select(fd + 1, &fds, NULL, NULL, &tv);
    if (r == -1) {
        return -1;
    }
    if (r == 0) {
        return -2; // timeout
    }

    struct v4l2_buffer buf;
    memset(&buf, 0, sizeof(buf));
    buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    buf.memory = V4L2_MEMORY_MMAP;

    if (ioctl(fd, VIDIOC_DQBUF, &buf) == -1) {
        return -1;
    }
    if (buf.index >= n_buffers) {
        return -1;
    }

    *out_ptr = buffers[buf.index].start;
    *out_len = buf.bytesused;
    *out_index = buf.index;

    return 0;
}

int uvc_enqueue(int fd, uint32_t index) {
    if (index >= n_buffers) {
        return -1;
    }

    struct v4l2_buffer buf;
    memset(&buf, 0, sizeof(buf));
    buf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
    buf.memory = V4L2_MEMORY_MMAP;
    buf.index = index;

    if (ioctl(fd, VIDIOC_QBUF, &buf) == -1) {
        return -1;
    }
    return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
)

const (
	FourCCMJPG = 0x47504A4D // 'MJPG' little-endian
	FourCCYUYV = 0x56595559 // 'YUYV' little-endian
)

// v4l2Device wraps the C V4L2 operations for a single device instance.
type v4l2Device struct {
	fd           C.int
	opened       bool
	mapped       bool
	bufferCount  uint
	lastSizeImg  uint32
}

func (d *v4l2Device) Open(path string) error {
	if d.opened {
		return nil
	}
	cpath := C.CString(path)
	defer C.free((C.VoidPtr)(cpath))
	fd := C.uvc_open(cpath)
	if fd < 0 {
		return fmt.Errorf("uvc_open failed for %s", path)
	}
	d.fd = fd
	d.opened = true
	return nil
}

func (d *v4l2Device) Close() error {
	if !d.opened {
		return nil
	}
	if d.mapped {
		C.uvc_uninit_mmap()
		d.mapped = false
	}
	if C.uvc_close(d.fd) != 0 {
		return errors.New("uvc_close failed")
	}
	d.opened = false
	return nil
}

func (d *v4l2Device) Configure(width, height int, pixfmt uint32, fps int) error {
	if !d.opened {
		return errors.New("device not open")
	}
	var size C.uint32_t
	if C.uvc_set_format(d.fd, C.uint32_t(width), C.uint32_t(height), C.uint32_t(pixfmt), &size) != 0 {
		return errors.New("VIDIOC_S_FMT failed")
	}
	d.lastSizeImg = uint32(size)
	if fps > 0 {
		if C.uvc_set_fps(d.fd, C.uint32_t(fps)) != 0 {
			// Some devices may not support FPS change; do not treat as fatal
			// but report error to caller to decide.
			return errors.New("VIDIOC_S_PARM failed (FPS)")
		}
	}
	return nil
}

func (d *v4l2Device) InitMMap(count int) error {
	if !d.opened {
		return errors.New("device not open")
	}
	if C.uvc_init_mmap(d.fd, C.uint(count)) != 0 {
		return errors.New("uvc_init_mmap failed")
	}
	d.mapped = true
	d.bufferCount = uint(count)
	return nil
}

func (d *v4l2Device) Start() error {
	if !d.opened {
		return errors.New("device not open")
	}
	if C.uvc_start_streaming(d.fd) != 0 {
		return errors.New("uvc_start_streaming failed")
	}
	return nil
}

func (d *v4l2Device) Stop() error {
	if !d.opened {
		return nil
	}
	C.uvc_stop_streaming(d.fd)
	C.uvc_uninit_mmap()
	d.mapped = false
	return nil
}

// Dequeue returns a frame pointer, length and buffer index. timeoutMs: -1 for blocking; otherwise finite timeout.
func (d *v4l2Device) Dequeue(timeoutMs int) (ptr unsafe.Pointer, length uint32, index uint32, timeout bool, err error) {
	if !d.opened {
		return nil, 0, 0, false, errors.New("device not open")
	}
	var p C.void_ptr
	var l C.uint32_t
	var idx C.uint32_t
	to := C.uint32_t(timeoutMs)
	ret := C.uvc_dequeue2(d.fd, &p, &l, to, &idx)
	if ret == -2 {
		return nil, 0, 0, true, nil
	}
	if ret != 0 {
		return nil, 0, 0, false, errors.New("uvc_dequeue2 failed")
	}
	return unsafe.Pointer(p), uint32(l), uint32(idx), false, nil
}

func (d *v4l2Device) Enqueue(index uint32) error {
	if C.uvc_enqueue(d.fd, C.uint32_t(index)) != 0 {
		return errors.New("uvc_enqueue failed")
	}
	return nil
}
