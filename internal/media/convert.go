package media

import (
	"runtime"
	"sync"

	"github.com/rm4n0s/errors"
)

// RGBAToI420 converts a packed RGBA frame to I420 using BT.601 limited-range
// coefficients. Odd dimensions are cropped by one pixel.
func RGBAToI420(src RGBAFrame) (I420Frame, error) {
	w, h := src.Width&^1, src.Height&^1
	if w <= 0 || h <= 0 || len(src.Data) < src.Width*src.Height*4 {
		return I420Frame{}, errors.New("InvalidFrameSize", "RGBA frame is empty or its buffer is too small",
			"width", src.Width, "height", src.Height, "bytes", len(src.Data))
	}
	dst, err := NewI420(w, h)
	if err != nil {
		return I420Frame{}, err
	}
	yPlane, uPlane, vPlane := dst.Planes()
	stride := src.Width * 4
	cw := w / 2

	parallelRows(h/2, func(r0, r1 int) {
		for cr := r0; cr < r1; cr++ {
			row0 := src.Data[(2*cr)*stride:]
			row1 := src.Data[(2*cr+1)*stride:]
			y0 := yPlane[(2*cr)*w:]
			y1 := yPlane[(2*cr+1)*w:]
			for cx := 0; cx < cw; cx++ {
				var rs, gs, bs int
				for dy := 0; dy < 2; dy++ {
					row, yr := row0, y0
					if dy == 1 {
						row, yr = row1, y1
					}
					for dx := 0; dx < 2; dx++ {
						i := (2*cx + dx) * 4
						r, g, b := int(row[i]), int(row[i+1]), int(row[i+2])
						yr[2*cx+dx] = clampByte(((66*r + 129*g + 25*b + 128) >> 8) + 16)
						rs += r
						gs += g
						bs += b
					}
				}
				r, g, b := rs>>2, gs>>2, bs>>2
				uPlane[cr*cw+cx] = clampByte(((-38*r - 74*g + 112*b + 128) >> 8) + 128)
				vPlane[cr*cw+cx] = clampByte(((112*r - 94*g - 18*b + 128) >> 8) + 128)
			}
		}
	})
	return dst, nil
}

// I420ToRGBA converts an I420 frame to packed RGBA (alpha is 255).
func I420ToRGBA(src I420Frame) (RGBAFrame, error) {
	if !src.Valid() {
		return RGBAFrame{}, errors.New("InvalidFrameSize", "I420 frame is invalid",
			"width", src.Width, "height", src.Height, "bytes", len(src.Data))
	}
	w, h := src.Width, src.Height
	yPlane, uPlane, vPlane := src.Planes()
	dst := RGBAFrame{Width: w, Height: h, Data: make([]byte, w*h*4)}
	cw := w / 2

	parallelRows(h, func(r0, r1 int) {
		for row := r0; row < r1; row++ {
			out := dst.Data[row*w*4:]
			for x := 0; x < w; x++ {
				c := 298 * (int(yPlane[row*w+x]) - 16)
				d := int(uPlane[(row/2)*cw+x/2]) - 128
				e := int(vPlane[(row/2)*cw+x/2]) - 128
				i := x * 4
				out[i] = clampByte((c + 409*e + 128) >> 8)
				out[i+1] = clampByte((c - 100*d - 208*e + 128) >> 8)
				out[i+2] = clampByte((c + 516*d + 128) >> 8)
				out[i+3] = 255
			}
		}
	})
	return dst, nil
}

func clampByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// parallelRows splits [0,rows) into chunks processed by up to NumCPU goroutines.
func parallelRows(rows int, fn func(r0, r1 int)) {
	workers := runtime.NumCPU()
	if workers > rows {
		workers = rows
	}
	if workers <= 1 {
		fn(0, rows)
		return
	}
	var wg sync.WaitGroup
	chunk := (rows + workers - 1) / workers
	for r0 := 0; r0 < rows; r0 += chunk {
		r1 := min(r0+chunk, rows)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(r0, r1)
		}()
	}
	wg.Wait()
}

// ShrinkRGBA returns f unchanged when it is at most maxWidth wide; otherwise it
// keeps every k-th pixel (k = ceil(width/maxWidth)) so the result fits.
func ShrinkRGBA(f RGBAFrame, maxWidth int) RGBAFrame {
	if maxWidth <= 0 || f.Width <= maxWidth || f.Width <= 0 || f.Height <= 0 {
		return f
	}
	k := (f.Width + maxWidth - 1) / maxWidth
	w, h := f.Width/k, f.Height/k
	out := RGBAFrame{Width: w, Height: h, Data: make([]byte, w*h*4)}
	parallelRows(h, func(r0, r1 int) {
		for y := r0; y < r1; y++ {
			src := f.Data[(y*k)*f.Width*4:]
			dst := out.Data[y*w*4:]
			for x := 0; x < w; x++ {
				copy(dst[x*4:x*4+4], src[(x*k)*4:(x*k)*4+4])
			}
		}
	})
	return out
}
