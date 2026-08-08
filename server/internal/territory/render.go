package territory

// 可视化工具（T8 验收）：ASCII 字符画与 PNG 渲染。仅测试/调试用，允许分配。

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
)

var glyphs = [...]byte{' ', '1', '2', '3', '4', '#'}

// ASCII 输出 96×96 字符画到 w。
func (f *Field) ASCII(w io.Writer) {
	for y := 0; y < f.h; y++ {
		for x := 0; x < f.w; x++ {
			o := f.owner[y*f.w+x]
			if o == OwnerWall {
				o = 5
			}
			_, _ = w.Write([]byte{glyphs[o]})
		}
		_, _ = fmt.Fprintln(w)
	}
}

var palette = [6]color.RGBA{
	{0xE8, 0xB0, 0x30, 0xFF}, // 0 原汤
	{0xE0, 0x40, 0x40, 0xFF}, // 1
	{0x40, 0x90, 0xE0, 0xFF}, // 2
	{0x40, 0xC0, 0x60, 0xFF}, // 3
	{0xC0, 0x60, 0xC0, 0xFF}, // 4
	{0x30, 0x20, 0x10, 0xFF}, // 15 锅外
}

// PNG 输出调色板渲染到 path（scale 为每格像素数）。
func (f *Field) PNG(path string, scale int) error {
	img := image.NewRGBA(image.Rect(0, 0, f.w*scale, f.h*scale))
	for y := 0; y < f.h; y++ {
		for x := 0; x < f.w; x++ {
			o := f.owner[y*f.w+x]
			if o == OwnerWall {
				o = 5
			}
			c := palette[o]
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetRGBA(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, img)
}
