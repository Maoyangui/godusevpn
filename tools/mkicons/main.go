// mkicons 生成三端要用的图标:Windows 的 ICO、macOS / Linux 用的 appicon.png,以及托盘的三个状态图标
// (连接中绿点、未连接灰点、出错红点)。ICO 里直接放 PNG 条目(Vista 起支持),不用自己编码 BMP。
//
// 应用图标合成白色圆角底,和安卓那边的自适应图标(白底 + logo 前景)看着是同一个东西;
// 托盘图标保持透明,否则任务栏上会顶出来一个白方块。16 / 20 这两档太小,加了底 logo 就糊了,也保持透明。
//
//	go run ./tools/mkicons -logo cmd/godusevpn/build/logo.png -out cmd/godusevpn/build
//	go run ./tools/mkicons -in <各尺寸 png 目录> -out cmd/godusevpn/build   # 旧用法,仍然可以
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

// bgFrom 应用图标从这一档起加底;再小就只剩糊成一团的 logo,不如留透明的。
const bgFrom = 24

// logoScale logo 在底板上占的比例:安卓自适应图标的安全区是 66/108,这里留得比它松一点,
// 因为我们的底板不会被启动器再裁一圈。
const logoScale = 0.80

func main() {
	in := flag.String("in", ".", "PNG 目录(logo-<size>.png);给了 -logo 就不用它")
	logo := flag.String("logo", "", "单张透明 logo PNG,按它缩出各个尺寸")
	bg := flag.String("bg", "#ffffff", "应用图标的底色(#rrggbb);none = 保持透明")
	out := flag.String("out", "cmd/godusevpn/build", "输出目录")
	flag.Parse()
	must(os.MkdirAll(filepath.Join(*out, "windows"), 0o755))
	must(os.MkdirAll(filepath.Join(*out, "tray"), 0o755))

	imgs := map[int]*image.NRGBA{}
	if *logo != "" {
		src := toNRGBA(mustImg(png.Decode(mustFile(os.Open(*logo)))))
		for _, s := range sizes {
			imgs[s] = scale(src, s)
		}
	} else {
		for _, s := range sizes {
			f, err := os.Open(filepath.Join(*in, fmt.Sprintf("logo-%d.png", s)))
			must(err)
			im, err := png.Decode(f)
			f.Close()
			must(err)
			imgs[s] = toNRGBA(im)
		}
	}
	// 应用图标:全尺寸,够大的档位垫上底板
	c, opaque := parseHex(*bg)
	app := map[int]*image.NRGBA{}
	for _, s := range sizes {
		if opaque && s >= bgFrom {
			app[s] = onPlate(imgs[s], c)
		} else {
			app[s] = imgs[s]
		}
	}
	must(writeICO(filepath.Join(*out, "windows", "icon.ico"), app, sizes))
	must(writePNG(filepath.Join(*out, "appicon.png"), app[256]))
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

// parseHex 读 #rrggbb;"none" 或读不动就当作不要底板。
func parseHex(s string) (color.NRGBA, bool) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, false
	}
	var r, g, b int
	if _, err := fmt.Sscanf(s[1:], "%02x%02x%02x", &r, &g, &b); err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{uint8(r), uint8(g), uint8(b), 0xff}, true
}

// scale 面积平均缩放:一路从 256 缩到 16 也不会糊,不用额外拉一个图像库进来。
func scale(src *image.NRGBA, n int) *image.NRGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		y0, y1 := y*sh/n, (y+1)*sh/n
		if y1 == y0 {
			y1 = y0 + 1
		}
		for x := 0; x < n; x++ {
			x0, x1 := x*sw/n, (x+1)*sw/n
			if x1 == x0 {
				x1 = x0 + 1
			}
			var r, g, b, a, cnt float64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					p := src.NRGBAAt(sx, sy)
					af := float64(p.A) / 255
					r += float64(p.R) * af // 先乘上不透明度再平均,边缘才不会带出一圈脏色
					g += float64(p.G) * af
					b += float64(p.B) * af
					a += float64(p.A)
					cnt++
				}
			}
			if a <= 0 {
				continue
			}
			aa := a / cnt
			dst.SetNRGBA(x, y, color.NRGBA{
				uint8(clamp(r / cnt / (aa / 255))), uint8(clamp(g / cnt / (aa / 255))),
				uint8(clamp(b / cnt / (aa / 255))), uint8(clamp(aa)),
			})
		}
	}
	return dst
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// onPlate 把 logo 摆到一块圆角底板上。圆角边缘按 3×3 超采样求覆盖率,小尺寸下也不会出锯齿。
func onPlate(src *image.NRGBA, c color.NRGBA) *image.NRGBA {
	n := src.Bounds().Dx()
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	rad := float64(n) * 0.22
	fn := float64(n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			hit := 0
			for sy := 0; sy < 3; sy++ {
				for sx := 0; sx < 3; sx++ {
					px, py := float64(x)+(float64(sx)+0.5)/3, float64(y)+(float64(sy)+0.5)/3
					if inRounded(px, py, fn, rad) {
						hit++
					}
				}
			}
			if hit > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(hit * 255 / 9)})
			}
		}
	}
	inner := scale(src, int(float64(n)*logoScale+0.5))
	off := (n - inner.Bounds().Dx()) / 2
	draw.Draw(dst, inner.Bounds().Add(image.Pt(off, off)), inner, image.Point{}, draw.Over)
	return dst
}

// inRounded 点在不在圆角方里(四个角按圆算,其余按方算)。
func inRounded(x, y, n, r float64) bool {
	if x < 0 || y < 0 || x > n || y > n {
		return false
	}
	cx, cy := x, y
	switch {
	case x < r:
		cx = r
	case x > n-r:
		cx = n - r
	default:
		return true
	}
	switch {
	case y < r:
		cy = r
	case y > n-r:
		cy = n - r
	default:
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func mustFile(f *os.File, err error) *os.File { must(err); return f }
func mustImg(im image.Image, err error) image.Image {
	must(err)
	return im
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
