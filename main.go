package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const TileSize = 1000

type Config struct {
	Zoom       int
	Fullsize   string
	TileRange  string
	SingleTile string
	Versions   string
	OutputFile string
	CacheDir   string
	AutoFetch  bool
}

func parseFullsize(fs string) (startX, startY, width, height int, err error) {
	parts := strings.Split(fs, "-")
	switch len(parts) {
	case 6:
		vals := make([]int, 6)
		for i, p := range parts {
			vals[i], err = strconv.Atoi(p)
			if err != nil {
				return
			}
		}
		startX = vals[0]*TileSize + vals[2]
		startY = vals[1]*TileSize + vals[3]
		width = vals[4]
		height = vals[5]
	case 8:
		vals := make([]int, 8)
		for i, p := range parts {
			vals[i], err = strconv.Atoi(p)
			if err != nil {
				return
			}
		}
		x1 := vals[0]*TileSize + vals[2]
		y1 := vals[1]*TileSize + vals[3]
		x2 := vals[4]*TileSize + vals[6]
		y2 := vals[5]*TileSize + vals[7]
		if x1 > x2 { x1, x2 = x2, x1 }
		if y1 > y2 { y1, y2 = y2, y1 }
		startX = x1
		startY = y1
		width = x2 - x1
		height = y2 - y1
	default:
		err = fmt.Errorf("invalid fullsize format: %s", fs)
	}
	return
}

func parseTileRange(tr string) (minTX, minTY, maxTX, maxTY int, err error) {
	parts := strings.Split(tr, "_")
	if len(parts) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("invalid tile range format: %s", tr)
	}
	p1 := strings.Split(parts[0], "-")
	p2 := strings.Split(parts[1], "-")
	if len(p1) != 2 || len(p2) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("invalid tile range points: %s", tr)
	}
	x1, _ := strconv.Atoi(p1[0])
	y1, _ := strconv.Atoi(p1[1])
	x2, _ := strconv.Atoi(p2[0])
	y2, _ := strconv.Atoi(p2[1])
	if x1 > x2 { x1, x2 = x2, x1 }
	if y1 > y2 { y1, y2 = y2, y1 }
	return x1, y1, x2, y2, nil
}

func fetchVersionsFromSite() ([]string, error) {
	resp, err := http.Get("https://wplace.eralyon.net/")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`version:\s*'([^']+)'`)
	matches := re.FindAllStringSubmatch(string(body), -1)

	var versions []string
	for _, match := range matches {
		if len(match) > 1 {
			versions = append(versions, match[1])
		}
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions found in site HTML")
	}

	return versions, nil
}

func downloadRawTile(version, cacheDir string, zoom, x, y int) (image.Image, error) {
	vStr := version
	if !strings.HasPrefix(vStr, "v") {
		vStr = "v" + vStr
	}

	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%s_%d_%d_%d.png", vStr, zoom, x, y))
	if _, err := os.Stat(cachePath); err == nil {
		f, err := os.Open(cachePath)
		if err == nil {
			img, err := png.Decode(f)
			f.Close()
			if err == nil {
				return img, nil
			}
		}
	}

	url := fmt.Sprintf("https://wplace.eralyon.net/tiles/%s/%d/%d/%d.png", vStr, zoom, x, y)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	img, err := png.Decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("PNG decode error for %s: %v", url, err)
	}

	if err := os.MkdirAll(cacheDir, 0755); err == nil {
		f, err := os.Create(cachePath)
		if err == nil {
			png.Encode(f, img)
			f.Close()
		}
	}
	return img, nil
}

func downloadMergedTile(version, cacheDir string, zoom, x, y int) (image.Image, error) {
	if !strings.Contains(version, ".") {
		return downloadRawTile(version, cacheDir, zoom, x, y)
	}
	baseVersion := strings.Split(version, ".")[0]
	baseImg, err := downloadRawTile(baseVersion, cacheDir, zoom, x, y)
	diffImg, err2 := downloadRawTile(version, cacheDir, zoom, x, y)
	if err != nil && err2 != nil {
		return nil, fmt.Errorf("failed to download both base and diff for %s", version)
	}
	if err == nil && err2 != nil {
		return baseImg, nil
	}
	if err != nil && err2 == nil {
		return diffImg, nil
	}
	bounds := baseImg.Bounds()
	merged := image.NewRGBA(bounds)
	draw.Draw(merged, bounds, baseImg, bounds.Min, draw.Src)
	draw.Draw(merged, bounds, diffImg, bounds.Min, draw.Over)
	return merged, nil
}

func readVersions(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var versions []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			versions = append(versions, line)
		}
	}
	return versions, scanner.Err()
}

func interactiveMode() Config {
	reader := bufio.NewReader(os.Stdin)
	config := Config{
		Zoom:       11,
		Versions:   "versions.txt",
		OutputFile: "heatmap.png",
		CacheDir:   "tile_cache",
		AutoFetch:  true,
	}

	fmt.Println("=== Wplace Heatmap Generator (Interactive Mode) ===")
	
	fmt.Print("Fetch versions automatically from wplace.eralyon.net? [Y/n]: ")
	autoStr, _ := reader.ReadString('\n')
	autoStr = strings.ToLower(strings.TrimSpace(autoStr))
	if autoStr == "n" {
		config.AutoFetch = false
	}

	fmt.Println("Select Coordinate Mode:")
	fmt.Println("1. Fullsize (6 parts: tileX-tileY-pixelX-pixelY-width-height)")
	fmt.Println("2. Fullsize (8 parts: tileX1-tileY1-pixelX1-pixelY1-tileX2-tileY2-pixelX2-pixelY2)")
	fmt.Println("3. Tile Range (minTX-minTY_maxTX-maxTY)")
	fmt.Println("4. Single Tile (tileX-tileY)")
	fmt.Print("Choice [1-4]: ")
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)

	switch choice {
	case "1", "2":
		fmt.Print("Enter coordinate string: ")
		val, _ := reader.ReadString('\n')
		config.Fullsize = strings.TrimSpace(val)
	case "3":
		fmt.Print("Enter tile range (e.g. 1818-806_1819-806): ")
		val, _ := reader.ReadString('\n')
		config.TileRange = strings.TrimSpace(val)
	case "4":
		fmt.Print("Enter tile (e.g. 1818-806): ")
		val, _ := reader.ReadString('\n')
		config.SingleTile = strings.TrimSpace(val)
	default:
		fmt.Println("Invalid choice, exiting.")
		os.Exit(1)
	}

	fmt.Print("Output filename [heatmap.png]: ")
	out, _ := reader.ReadString('\n')
	out = strings.TrimSpace(out)
	if out != "" {
		config.OutputFile = out
	}

	return config
}

func main() {
	config := Config{}
	flag.IntVar(&config.Zoom, "zoom", 11, "Zoom level")
	flag.StringVar(&config.Fullsize, "fullsize", "", "Fullsize range (6 or 8 parts)")
	flag.StringVar(&config.TileRange, "tiles", "", "Tile range mode (minX-minY_maxX-maxY)")
	flag.StringVar(&config.SingleTile, "tile", "", "Single tile mode (tileX-tileY)")
	flag.StringVar(&config.Versions, "vfile", "versions.txt", "Versions file")
	flag.StringVar(&config.OutputFile, "out", "heatmap.png", "Output filename")
	flag.StringVar(&config.CacheDir, "cache", "tile_cache", "Tile cache directory")
	flag.BoolVar(&config.AutoFetch, "auto", true, "Automatically fetch versions from site")
	flag.Parse()

	if config.Fullsize == "" && config.TileRange == "" && config.SingleTile == "" && flag.NFlag() == 0 {
		config = interactiveMode()
	}

	var versions []string
	var err error
	if config.AutoFetch {
		fmt.Print("Fetching versions from wplace.eralyon.net...")
		versions, err = fetchVersionsFromSite()
		if err != nil {
			fmt.Printf("\nWarning: Auto-fetch failed: %v. Falling back to file.\n", err)
			config.AutoFetch = false
		} else {
			fmt.Printf(" Found %d versions.\n", len(versions))
		}
	}

	if !config.AutoFetch {
		versions, err = readVersions(config.Versions)
		if err != nil {
			log.Fatalf("Failed to read versions from file: %v", err)
		}
	}

	var startAbsX, startAbsY, width, height int
	if config.Fullsize != "" {
		startAbsX, startAbsY, width, height, err = parseFullsize(config.Fullsize)
		if err != nil {
			log.Fatalf("Parse error: %v", err)
		}
	} else if config.TileRange != "" {
		minTX, minTY, maxTX, maxTY, err := parseTileRange(config.TileRange)
		if err != nil {
			log.Fatalf("Parse error: %v", err)
		}
		startAbsX = minTX * TileSize
		startAbsY = minTY * TileSize
		width = (maxTX - minTX + 1) * TileSize
		height = (maxTY - minTY + 1) * TileSize
	} else if config.SingleTile != "" {
		parts := strings.Split(config.SingleTile, "-")
		if len(parts) != 2 {
			log.Fatalf("Invalid single tile format: %s", config.SingleTile)
		}
		tx, _ := strconv.Atoi(parts[0])
		ty, _ := strconv.Atoi(parts[1])
		startAbsX = tx * TileSize
		startAbsY = ty * TileSize
		width = TileSize
		height = TileSize
	} else {
		startAbsX = 1818 * TileSize
		startAbsY = 806 * TileSize
		width = 1000
		height = 1000
	}

	minTileX := startAbsX / TileSize
	minTileY := startAbsY / TileSize
	maxTileX := (startAbsX + width - 1) / TileSize
	maxTileY := (startAbsY + height - 1) / TileSize

	changes := make([][]uint32, height)
	for i := range changes {
		changes[i] = make([]uint32, width)
	}

	var prevCombined *image.RGBA
	successCount := 0

	fmt.Printf("\nGenerating heatmap: %dx%d px (Tiles %d,%d to %d,%d)\n", width, height, minTileX, minTileY, maxTileX, maxTileY)

	for _, v := range versions {
		currentCombined := image.NewRGBA(image.Rect(0, 0, width, height))
		versionValid := true
		for tx := minTileX; tx <= maxTileX; tx++ {
			for ty := minTileY; ty <= maxTileY; ty++ {
				img, err := downloadMergedTile(v, config.CacheDir, config.Zoom, tx, ty)
				if err != nil {
					versionValid = false
					break
				}
				tileRect := image.Rect(tx*TileSize, ty*TileSize, (tx+1)*TileSize, (ty+1)*TileSize)
				targetRect := image.Rect(startAbsX, startAbsY, startAbsX+width, startAbsY+height)
				inter := tileRect.Intersect(targetRect)
				if !inter.Empty() {
					drawX := inter.Min.X - startAbsX
					drawY := inter.Min.Y - startAbsY
					srcX := inter.Min.X - tx*TileSize
					srcY := inter.Min.Y - ty*TileSize
					draw.Draw(currentCombined, image.Rect(drawX, drawY, drawX+inter.Dx(), drawY+inter.Dy()), 
						img, image.Point{srcX, srcY}, draw.Src)
				}
			}
			if !versionValid { break }
		}
		if !versionValid { continue }
		successCount++
		fmt.Printf("\rProcessed: %d/%d (%s)", successCount, len(versions), v)
		if prevCombined != nil {
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					if !colorsEqual(currentCombined.At(x, y), prevCombined.At(x, y)) {
						changes[y][x]++
					}
				}
			}
		}
		prevCombined = currentCombined
	}
	fmt.Println("\nNormalization and saving...")
	var maxChanges uint32
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if changes[y][x] > maxChanges {
				maxChanges = changes[y][x]
			}
		}
	}
	heatmap := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			heatmap.Set(x, y, getHeatColor(changes[y][x], maxChanges))
		}
	}
	outFile, err := os.Create(config.OutputFile)
	if err != nil {
		log.Fatal(err)
	}
	png.Encode(outFile, heatmap)
	outFile.Close()
	fmt.Printf("Done! Saved to %s (Max changes: %d)\n", config.OutputFile, maxChanges)
}

func colorsEqual(c1, c2 color.Color) bool {
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, a2 := c2.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}

func getHeatColor(val, max uint32) color.Color {
	if val == 0 { return color.RGBA{0, 0, 0, 255} }
	ratio := float64(val) / float64(max)
	var r, g, b float64
	if ratio < 0.25 {
		b = (ratio / 0.25) * 255
	} else if ratio < 0.5 {
		b = 255 - ((ratio - 0.25) / 0.25) * 255
		g = ((ratio - 0.25) / 0.25) * 255
	} else if ratio < 0.75 {
		g = 255
		r = ((ratio - 0.5) / 0.25) * 255
	} else {
		r = 255
		g = 255 - ((ratio - 0.75) / 0.25) * 255
	}
	return color.RGBA{uint8(math.Round(r)), uint8(math.Round(g)), uint8(math.Round(b)), 255}
}
