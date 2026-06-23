//go:build ignore

// Generates the tracker's placeholder app icons as valid RGBA PNGs (Tauri requires RGBA) plus a
// Windows .ico (a 256x256 PNG wrapped in the ICO container). Run from src-tauri:
//   go run tools/genicon.go
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
)

func render(sz int) *image.RGBA {
	// Alpha 254 (not 255): Tauri requires RGBA icons, but Go's png.Encode drops the alpha channel
	// for a fully-opaque image (emitting RGB). A near-opaque pixel keeps it RGBA — imperceptible.
	brand := color.RGBA{R: 30, G: 58, B: 138, A: 254}  // deep indigo
	accent := color.RGBA{R: 99, G: 102, B: 241, A: 254} // a diagonal accent band
	img := image.NewRGBA(image.Rect(0, 0, sz, sz))
	for y := 0; y < sz; y++ {
		for x := 0; x < sz; x++ {
			c := brand
			if x+y > sz/2 && x+y < sz {
				c = accent
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func writePNG(path string, sz int) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	if err := png.Encode(f, render(sz)); err != nil {
		panic(err)
	}
	_ = f.Close()
}

func writeICO(path string) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, render(256)); err != nil {
		panic(err)
	}
	data := pngBuf.Bytes()
	var ico bytes.Buffer
	_ = binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, 1}) // reserved, type=icon, count=1
	ico.Write([]byte{0, 0, 0, 0})                                  // width=0(256), height=0(256), colors=0, reserved=0
	_ = binary.Write(&ico, binary.LittleEndian, uint16(1))         // color planes
	_ = binary.Write(&ico, binary.LittleEndian, uint16(32))        // bits per pixel
	_ = binary.Write(&ico, binary.LittleEndian, uint32(len(data))) // image byte size
	_ = binary.Write(&ico, binary.LittleEndian, uint32(22))        // offset (6-byte header + 16-byte entry)
	ico.Write(data)
	if err := os.WriteFile(path, ico.Bytes(), 0o644); err != nil {
		panic(err)
	}
}

func main() {
	if err := os.MkdirAll("icons", 0o755); err != nil {
		panic(err)
	}
	writePNG("icons/icon.png", 512)
	writePNG("icons/128x128.png", 128)
	writePNG("icons/32x32.png", 32)
	writeICO("icons/icon.ico")
}
