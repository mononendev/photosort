// Package dhash computes a 64-bit difference hash of an image.
package dhash

import (
	"fmt"
	"image"
	"image/color"
	"math/bits"
	"strconv"
)

// Hash returns the 64-bit dHash of img: the image is averaged into a 9x8
// grayscale grid and bit row*8+col is set when cell(col,row) < cell(col+1,row).
func Hash(img image.Image) uint64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 0
	}
	var cells [8][9]float64
	for row := 0; row < 8; row++ {
		y0, y1 := row*h/8, (row+1)*h/8
		for col := 0; col < 9; col++ {
			x0, x1 := col*w/9, (col+1)*w/9
			if x1 <= x0 || y1 <= y0 {
				cells[row][col] = gray(img, b.Min.X+x0, b.Min.Y+y0)
				continue
			}
			var sum float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					sum += gray(img, b.Min.X+x, b.Min.Y+y)
				}
			}
			cells[row][col] = sum / float64((x1-x0)*(y1-y0))
		}
	}
	var out uint64
	for row := 0; row < 8; row++ {
		for col := 0; col < 8; col++ {
			if cells[row][col] < cells[row][col+1] {
				out |= 1 << uint(row*8+col)
			}
		}
	}
	return out
}

func gray(img image.Image, x, y int) float64 {
	return float64(color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y)
}

// Distance returns the Hamming distance between two hashes.
func Distance(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// Hex formats h as 16 lowercase zero-padded hex digits.
func Hex(h uint64) string { return fmt.Sprintf("%016x", h) }

// ParseHex parses the output of Hex.
func ParseHex(s string) (uint64, error) {
	if len(s) != 16 {
		return 0, fmt.Errorf("dhash: want 16 hex digits, got %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("dhash: parse %q: %w", s, err)
	}
	return v, nil
}
