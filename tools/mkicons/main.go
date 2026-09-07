// mkicons 把栅格化好的 logo PNG(16/20/24/32/48/64/128/256)打成 Windows ICO,并生成托盘的三个状态图标:
// 连接中(右下角绿点)、未连接(灰点)、出错(红点)。ICO 里直接放 PNG 条目(Vista 起支持),不用自己编码 BMP。
//
//	go run ./tools/mkicons -in <png目录> -out cmd/godusevpn/build
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
)

var sizes = []int{16, 20, 24, 32, 48, 64, 128, 256}

func main() {
	in := flag.String("in", ".", "PNG 目录(logo-<size>.png)")
	out := flag.String("out", "cmd/godusevpn/build", "输出目录")
	flag.Parse()
	must(os.MkdirAll(filepath.Join(*out, "windows"), 0o755))
	must(os.MkdirAll(filepath.Join(*out, "tray"), 0o755))

	imgs := map[int]*image.NRGBA{}
	for _, s := range sizes {
		f, err := os.Open(filepath.Join(*in, fmt.Sprintf("logo-%d.png", s)))
		must(err)
		im, err := png.Decode(f)
		f.Close()
		must(err)
		imgs[s] = toNRGBA(im)
	}
	// 应用图标:全尺寸
	must(writeICO(filepath.Join(*out, "windows", "icon.ico"), imgs, sizes))
	must(writePNG(filepath.Join(*out, "appicon.png"), imgs[256]))
	// 托盘图标:16/20/24/32 四档,各带状态点
	states := map[string]color.NRGBA{
		"on":  {0x22, 0xc5, 0x5e, 0xff}, // 绿
		"off": {0x9c, 0xa3, 0xaf, 0xff}, // 灰
		"err": {0xef, 0x44, 0x44, 0xff}, // 红
	}
	traySizes := []int{16, 20, 24, 32}
	for name, c := range states {
		badged := map[int]*image.NRGBA{}
		for _, s := range traySizes {
			badged[s] = withDot(imgs[s], c)
		}
		must(writeICO(filepath.Join(*out, "tray", name+".ico"), badged, traySizes))
	}
	fmt.Println("icons written to", *out)
}

func toNRGBA(im image.Image) *image.NRGBA {
	b := im.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), im, b.Min, draw.Src)
	return dst
}

// withDot 右下角画一个实心圆点(带一圈透明描边,深浅底都看得清)。
func withDot(src *image.NRGBA, c color.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, image.Point{}, draw.Src)
	n := dst.Bounds().Dx()
	r := float64(n) * 0.19
	cx, cy := float64(n)-r-1, float64(n)-r-1
	ring := r + float64(n)*0.06
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := dx*dx + dy*dy
			switch {
			case d <= r*r:
				dst.SetNRGBA(x, y, c)
			case d <= ring*ring:
				dst.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0}) // 描边:挖空一圈
			}
		}
	}
	return dst
}

func writePNG(path string, im image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, im)
}

// writeICO ICONDIR + N × ICONDIRENTRY + N × PNG 数据。
func writeICO(path string, imgs map[int]*image.NRGBA, order []int) error {
	var blobs [][]byte
	for _, s := range order {
		var buf bytes.Buffer
		if err := png.Encode(&buf, imgs[s]); err != nil {
			return err
		}
		blobs = append(blobs, buf.Bytes())
	}
	var out bytes.Buffer
	hdr := []any{uint16(0), uint16(1), uint16(len(order))}
	for _, v := range hdr {
		binary.Write(&out, binary.LittleEndian, v)
	}
	offset := 6 + 16*len(order)
	for i, s := range order {
		dim := byte(s)
		if s >= 256 {
			dim = 0 // 0 表示 256
		}
		entry := []any{dim, dim, byte(0), byte(0), uint16(1), uint16(32), uint32(len(blobs[i])), uint32(offset)}
		for _, v := range entry {
			binary.Write(&out, binary.LittleEndian, v)
		}
		offset += len(blobs[i])
	}
	for _, b := range blobs {
		out.Write(b)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
