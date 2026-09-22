// Package qrcode 是一個「只用標準函式庫」的 QR code 產生器,存在的唯一理由
// 是讓 GoNAS 的兩步驟驗證(TOTP)設定畫面能直接顯示一張 QR code,讓使用者
// 用手機驗證器 App 掃一下就完成設定,不用手動逐字輸入密鑰。
//
// 為什麼自己寫、而不是拉一個現成套件:這個專案刻意維持「單一靜態執行檔、
// 零第三方相依、離線也能跑」(見 internal/docker / internal/state 的說明),
// 而且這台 NAS 本來就常常沒有對外網路。QR 的編碼規則(ISO/IEC 18004)是
// 固定的演算法,自己實作一份、再用真的 QR 解碼器(zbar)回頭驗證每個版本
// 都掃得出來(見 qrcode_test.go 與交付時的解碼驗證),比在執行期依賴一個
// 抓不到的套件可靠。
//
// 範圍刻意收窄到「剛好夠用」:只做 byte(8-bit)模式、固定用 M 級錯誤更正、
// 支援版本 1–10。otpauth:// URI 大約 100–160 個位元組,byte 模式 M 級到
// 版本 8–9 就裝得下;支援到版本 10(M 級可裝 216 個資料碼字)留了餘裕給
// 比較長的帳號/發行者名稱。超出範圍會回明確錯誤,不會產生一張掃不出來的
// 半成品 QR。
package qrcode

import (
	"errors"
	"fmt"
)

// Matrix 是產生出來的 QR 模組矩陣,true=深色(黑)、false=淺色(白)。
// 不含 quiet zone(靜區);轉成圖時再自己加邊界(見 SVG)。
type Matrix struct {
	Size    int
	modules [][]bool
	// reserved 標記哪些格子屬於「功能圖樣/格式資訊」區,不能被資料或遮罩動到。
	reserved [][]bool
}

// At 回傳 (r,c) 是不是深色模組。
func (m *Matrix) At(r, c int) bool { return m.modules[r][c] }

// ---- 版本/錯誤更正參數表(只列 M 級,版本 1–10)---------------------------
//
// 每個版本一筆:ecPerBlock=每個區塊的錯誤更正碼字數;g1Blocks/g1Data 與
// g2Blocks/g2Data 是「群組一/群組二」的區塊數與每區塊資料碼字數(QR 規格
// 把資料碼字分成兩種大小的區塊)。總資料碼字 = g1Blocks*g1Data +
// g2Blocks*g2Data。數字直接取自 ISO/IEC 18004 的表 9(M 級那幾欄)。
type ecParams struct {
	ecPerBlock int
	g1Blocks   int
	g1Data     int
	g2Blocks   int
	g2Data     int
}

var ecTableM = map[int]ecParams{
	1:  {10, 1, 16, 0, 0},
	2:  {16, 1, 28, 0, 0},
	3:  {26, 1, 44, 0, 0},
	4:  {18, 2, 32, 0, 0},
	5:  {24, 2, 43, 0, 0},
	6:  {16, 4, 27, 0, 0},
	7:  {18, 4, 31, 0, 0},
	8:  {22, 2, 38, 2, 39},
	9:  {22, 3, 36, 2, 37},
	10: {26, 4, 43, 1, 44},
}

// alignPositions 是每個版本的對齊圖樣「中心座標」清單(列/行共用同一組)。
// 版本 1 沒有對齊圖樣。取自 ISO/IEC 18004 附錄 E。
var alignPositions = map[int][]int{
	1:  {},
	2:  {6, 18},
	3:  {6, 22},
	4:  {6, 26},
	5:  {6, 30},
	6:  {6, 34},
	7:  {6, 22, 38},
	8:  {6, 24, 42},
	9:  {6, 26, 46},
	10: {6, 28, 50},
}

func (p ecParams) totalDataCodewords() int {
	return p.g1Blocks*p.g1Data + p.g2Blocks*p.g2Data
}

// ---- GF(256) 伽羅瓦體運算(Reed–Solomon 用)------------------------------
// QR 用的是 GF(2^8),本原多項式 0x11D。先建 log/antilog 表。
var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGeneratorPoly 產生 degree=ecLen 的 Reed–Solomon 生成多項式係數。
func rsGeneratorPoly(ecLen int) []byte {
	g := []byte{1}
	for i := 0; i < ecLen; i++ {
		// g = g * (x - α^i)
		next := make([]byte, len(g)+1)
		for j := 0; j < len(g); j++ {
			next[j] ^= g[j]
			next[j+1] ^= gfMul(g[j], gfExp[i])
		}
		g = next
	}
	return g
}

// rsEncode 對 data 算出 ecLen 個錯誤更正碼字。
func rsEncode(data []byte, ecLen int) []byte {
	gen := rsGeneratorPoly(ecLen)
	rem := make([]byte, len(data)+ecLen)
	copy(rem, data)
	for i := 0; i < len(data); i++ {
		coef := rem[i]
		if coef == 0 {
			continue
		}
		for j := 0; j < len(gen); j++ {
			rem[i+j] ^= gfMul(gen[j], coef)
		}
	}
	return rem[len(data):]
}

// ---- 位元串小工具 --------------------------------------------------------
type bitBuffer struct {
	bits []bool
}

func (b *bitBuffer) append(val, n int) {
	for i := n - 1; i >= 0; i-- {
		b.bits = append(b.bits, (val>>i)&1 == 1)
	}
}
func (b *bitBuffer) len() int { return len(b.bits) }

// Encode 把 text 編成 byte 模式、M 級的 QR 矩陣。text 通常是 otpauth:// URI。
func Encode(text string) (*Matrix, error) {
	data := []byte(text)

	// 選最小的、裝得下的版本(1–10)。
	version := 0
	var params ecParams
	for v := 1; v <= 10; v++ {
		p := ecTableM[v]
		// 標頭:模式指示元 4 bits + 字元數(版本 1–9 是 8 bits,10 起是 16 bits)。
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		need := 4 + countBits + len(data)*8
		if need <= p.totalDataCodewords()*8 {
			version = v
			params = p
			break
		}
	}
	if version == 0 {
		return nil, fmt.Errorf("qrcode: data too long (%d bytes) for the supported versions (max is version 10, M level)", len(data))
	}

	// --- 組資料位元串 ---
	countBits := 8
	if version >= 10 {
		countBits = 16
	}
	var bb bitBuffer
	bb.append(0b0100, 4) // byte 模式
	bb.append(len(data), countBits)
	for _, d := range data {
		bb.append(int(d), 8)
	}
	totalDataCW := params.totalDataCodewords()
	capacityBits := totalDataCW * 8
	// 終止符:最多 4 個 0(不超過容量)。
	term := 4
	if bb.len()+term > capacityBits {
		term = capacityBits - bb.len()
	}
	bb.append(0, term)
	// 補到 byte 邊界。
	for bb.len()%8 != 0 {
		bb.bits = append(bb.bits, false)
	}
	// 轉成碼字。
	dataCW := make([]byte, 0, totalDataCW)
	for i := 0; i < bb.len(); i += 8 {
		var v byte
		for j := 0; j < 8; j++ {
			if bb.bits[i+j] {
				v |= 1 << (7 - j)
			}
		}
		dataCW = append(dataCW, v)
	}
	// 用 0xEC / 0x11 交替補滿。
	for len(dataCW) < totalDataCW {
		dataCW = append(dataCW, 0xEC)
		if len(dataCW) < totalDataCW {
			dataCW = append(dataCW, 0x11)
		}
	}

	// --- 分區塊、算 EC、交錯 ---
	type block struct{ data, ec []byte }
	var blocks []block
	idx := 0
	for i := 0; i < params.g1Blocks; i++ {
		d := dataCW[idx : idx+params.g1Data]
		idx += params.g1Data
		blocks = append(blocks, block{d, rsEncode(d, params.ecPerBlock)})
	}
	for i := 0; i < params.g2Blocks; i++ {
		d := dataCW[idx : idx+params.g2Data]
		idx += params.g2Data
		blocks = append(blocks, block{d, rsEncode(d, params.ecPerBlock)})
	}
	// 交錯資料碼字。
	var finalCW []byte
	maxData := params.g1Data
	if params.g2Data > maxData {
		maxData = params.g2Data
	}
	for i := 0; i < maxData; i++ {
		for _, bl := range blocks {
			if i < len(bl.data) {
				finalCW = append(finalCW, bl.data[i])
			}
		}
	}
	// 交錯 EC 碼字。
	for i := 0; i < params.ecPerBlock; i++ {
		for _, bl := range blocks {
			finalCW = append(finalCW, bl.ec[i])
		}
	}

	// 攤成位元串(給放模組用)。
	var msg []bool
	for _, cw := range finalCW {
		for i := 7; i >= 0; i-- {
			msg = append(msg, (cw>>i)&1 == 1)
		}
	}

	// --- 蓋模組 ---
	size := 17 + 4*version
	m := &Matrix{Size: size}
	m.modules = make([][]bool, size)
	m.reserved = make([][]bool, size)
	for i := range m.modules {
		m.modules[i] = make([]bool, size)
		m.reserved[i] = make([]bool, size)
	}
	m.placeFunctionPatterns(version)
	m.placeData(msg)

	best := m.applyBestMask()
	return best, nil
}

// set 設定一格,並標記為 reserved(功能區)。
func (m *Matrix) setFunc(r, c int, dark bool) {
	m.modules[r][c] = dark
	m.reserved[r][c] = true
}

func (m *Matrix) placeFinder(r, c int) {
	for dr := -1; dr <= 7; dr++ {
		for dc := -1; dc <= 7; dc++ {
			rr, cc := r+dr, c+dc
			if rr < 0 || rr >= m.Size || cc < 0 || cc >= m.Size {
				continue
			}
			// 7x7 的定位圖樣 + 一圈分隔的白邊。
			dark := false
			if dr >= 0 && dr <= 6 && dc >= 0 && dc <= 6 {
				if dr == 0 || dr == 6 || dc == 0 || dc == 6 || (dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4) {
					dark = true
				}
			}
			m.setFunc(rr, cc, dark)
		}
	}
}

func (m *Matrix) placeFunctionPatterns(version int) {
	// 三個定位圖樣(含分隔白邊)。
	m.placeFinder(0, 0)
	m.placeFinder(0, m.Size-7)
	m.placeFinder(m.Size-7, 0)

	// 時序圖樣(第 6 列/行的交替黑白)。
	for i := 8; i < m.Size-8; i++ {
		dark := i%2 == 0
		if !m.reserved[6][i] {
			m.setFunc(6, i, dark)
		}
		if !m.reserved[i][6] {
			m.setFunc(i, 6, dark)
		}
	}

	// 對齊圖樣(5x5)——不覆蓋定位圖樣角落。
	pos := alignPositions[version]
	for _, pr := range pos {
		for _, pc := range pos {
			// 跳過會撞到三個定位圖樣的位置。
			if (pr <= 8 && pc <= 8) || (pr <= 8 && pc >= m.Size-9) || (pr >= m.Size-9 && pc <= 8) {
				continue
			}
			for dr := -2; dr <= 2; dr++ {
				for dc := -2; dc <= 2; dc++ {
					dark := dr == -2 || dr == 2 || dc == -2 || dc == 2 || (dr == 0 && dc == 0)
					m.setFunc(pr+dr, pc+dc, dark)
				}
			}
		}
	}

	// 固定的深色模組。
	m.setFunc(m.Size-8, 8, true)

	// 保留格式資訊區(先佔位,值稍後在選好遮罩後填)。
	for i := 0; i < 9; i++ {
		if !m.reserved[8][i] {
			m.setFunc(8, i, false)
		}
		if !m.reserved[i][8] {
			m.setFunc(i, 8, false)
		}
	}
	for i := 0; i < 8; i++ {
		m.setFunc(8, m.Size-1-i, false)
		m.setFunc(m.Size-1-i, 8, false)
	}

	// 版本資訊區(版本 7 以上)——先保留,值稍後填。
	if version >= 7 {
		for i := 0; i < 6; i++ {
			for j := 0; j < 3; j++ {
				m.setFunc(i, m.Size-11+j, false)
				m.setFunc(m.Size-11+j, i, false)
			}
		}
	}
}

// placeData 用「之字形」把資料位元從右下往左上填進非保留格。
func (m *Matrix) placeData(msg []bool) {
	bit := 0
	up := true
	for col := m.Size - 1; col > 0; col -= 2 {
		if col == 6 { // 跳過時序行
			col--
		}
		for i := 0; i < m.Size; i++ {
			row := i
			if up {
				row = m.Size - 1 - i
			}
			for dc := 0; dc < 2; dc++ {
				c := col - dc
				if m.reserved[row][c] {
					continue
				}
				val := false
				if bit < len(msg) {
					val = msg[bit]
				}
				m.modules[row][c] = val
				bit++
			}
		}
		up = !up
	}
}

// maskFn 回傳第 k 種遮罩對 (r,c) 是否要反轉。
func maskFn(k, r, c int) bool {
	switch k {
	case 0:
		return (r+c)%2 == 0
	case 1:
		return r%2 == 0
	case 2:
		return c%3 == 0
	case 3:
		return (r+c)%3 == 0
	case 4:
		return (r/2+c/3)%2 == 0
	case 5:
		return (r*c)%2+(r*c)%3 == 0
	case 6:
		return ((r*c)%2+(r*c)%3)%2 == 0
	case 7:
		return ((r+c)%2+(r*c)%3)%2 == 0
	}
	return false
}

// applyBestMask 對 8 種遮罩各算一次懲罰分,選最低的那個,套用並填格式資訊。
func (m *Matrix) applyBestMask() *Matrix {
	bestScore := -1
	var best *Matrix
	bestMask := 0
	for k := 0; k < 8; k++ {
		cand := m.clone()
		for r := 0; r < cand.Size; r++ {
			for c := 0; c < cand.Size; c++ {
				if !cand.reserved[r][c] && maskFn(k, r, c) {
					cand.modules[r][c] = !cand.modules[r][c]
				}
			}
		}
		cand.placeFormatInfo(k)
		s := cand.penalty()
		if bestScore == -1 || s < bestScore {
			bestScore = s
			best = cand
			bestMask = k
		}
	}
	_ = bestMask
	return best
}

func (m *Matrix) clone() *Matrix {
	n := &Matrix{Size: m.Size}
	n.modules = make([][]bool, m.Size)
	n.reserved = make([][]bool, m.Size)
	for i := range m.modules {
		n.modules[i] = append([]bool(nil), m.modules[i]...)
		n.reserved[i] = append([]bool(nil), m.reserved[i]...)
	}
	return n
}

// placeFormatInfo 依 EC 級(M=00)與遮罩編號,算 15-bit BCH 格式資訊並填入。
func (m *Matrix) placeFormatInfo(mask int) {
	// M 級的 2-bit 指示是 0b00;加遮罩 3 bits。
	data := (0b00 << 3) | mask
	// 15-bit BCH(生成多項式 0x537),再 XOR 遮罩 0x5412。
	bch := data << 10
	for i := 14; i >= 10; i-- {
		if (bch>>i)&1 == 1 {
			bch ^= 0x537 << (i - 10)
		}
	}
	format := ((data << 10) | bch) ^ 0x5412

	bits := make([]bool, 15)
	for i := 0; i < 15; i++ {
		bits[i] = (format>>(14-i))&1 == 1
	}

	// 依 ISO/IEC 18004 §8.9 擺放 15 個格式位元的兩份副本。
	// 第一份:bit0..5 -> (8,0..5); bit6 -> (8,7); bit7 -> (8,8); bit8 -> (7,8);
	//         bit9..14 -> (5..0,8)
	m.modules[8][0] = bits[0]
	m.modules[8][1] = bits[1]
	m.modules[8][2] = bits[2]
	m.modules[8][3] = bits[3]
	m.modules[8][4] = bits[4]
	m.modules[8][5] = bits[5]
	m.modules[8][7] = bits[6]
	m.modules[8][8] = bits[7]
	m.modules[7][8] = bits[8]
	m.modules[5][8] = bits[9]
	m.modules[4][8] = bits[10]
	m.modules[3][8] = bits[11]
	m.modules[2][8] = bits[12]
	m.modules[1][8] = bits[13]
	m.modules[0][8] = bits[14]

	// 第二份:bit0..7 -> (Size-1..Size-8, 8);bit8..14 -> (8, Size-7..Size-1)
	for i := 0; i < 8; i++ {
		m.modules[m.Size-1-i][8] = bits[i]
	}
	for i := 0; i < 7; i++ {
		m.modules[8][m.Size-7+i] = bits[8+i]
	}
	// 固定深色模組要保持(可能被覆蓋),重設。
	m.modules[m.Size-8][8] = true

	// 版本資訊(版本 7+):18-bit BCH。
	m.placeVersionInfo()
}

// placeVersionInfo 對版本 7 以上填 18-bit 版本資訊(BCH 生成多項式 0x1F25)。
func (m *Matrix) placeVersionInfo() {
	version := (m.Size - 17) / 4
	if version < 7 {
		return
	}
	bch := version << 12
	for i := 17; i >= 12; i-- {
		if (bch>>i)&1 == 1 {
			bch ^= 0x1F25 << (i - 12)
		}
	}
	vinfo := (version << 12) | bch
	bits := make([]bool, 18)
	for i := 0; i < 18; i++ {
		bits[i] = (vinfo>>i)&1 == 1 // 低位在前(依規格)
	}
	// 左下 6x3 與右上 3x6。
	k := 0
	for i := 0; i < 6; i++ {
		for j := 0; j < 3; j++ {
			m.modules[m.Size-11+j][i] = bits[k]
			m.modules[i][m.Size-11+j] = bits[k]
			k++
		}
	}
}

// penalty 依 ISO/IEC 18004 §8.8.2 的四條規則算遮罩懲罰分。
func (m *Matrix) penalty() int {
	n := m.Size
	score := 0
	// 規則 1:同色連續 >=5。
	for r := 0; r < n; r++ {
		runC, runR := 1, 1
		for c := 1; c < n; c++ {
			if m.modules[r][c] == m.modules[r][c-1] {
				runC++
			} else {
				if runC >= 5 {
					score += 3 + (runC - 5)
				}
				runC = 1
			}
			if m.modules[c][r] == m.modules[c-1][r] {
				runR++
			} else {
				if runR >= 5 {
					score += 3 + (runR - 5)
				}
				runR = 1
			}
		}
		if runC >= 5 {
			score += 3 + (runC - 5)
		}
		if runR >= 5 {
			score += 3 + (runR - 5)
		}
	}
	// 規則 2:2x2 同色方塊。
	for r := 0; r < n-1; r++ {
		for c := 0; c < n-1; c++ {
			v := m.modules[r][c]
			if m.modules[r][c+1] == v && m.modules[r+1][c] == v && m.modules[r+1][c+1] == v {
				score += 3
			}
		}
	}
	// 規則 3:1:1:3:1:1 的定位圖樣狀樣式(前後接 4 個淺色)。
	pat1 := []bool{true, false, true, true, true, false, true, false, false, false, false}
	pat2 := []bool{false, false, false, false, true, false, true, true, true, false, true}
	for r := 0; r < n; r++ {
		for c := 0; c <= n-11; c++ {
			if matchPattern(m, r, c, pat1, true) || matchPattern(m, r, c, pat2, true) {
				score += 40
			}
			if matchPattern(m, c, r, pat1, false) || matchPattern(m, c, r, pat2, false) {
				score += 40
			}
		}
	}
	// 規則 4:深色比例偏離 50%。
	dark := 0
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			if m.modules[r][c] {
				dark++
			}
		}
	}
	total := n * n
	percent := dark * 100 / total
	dev := percent - 50
	if dev < 0 {
		dev = -dev
	}
	score += (dev / 5) * 10
	return score
}

func matchPattern(m *Matrix, r, c int, pat []bool, horiz bool) bool {
	for i := 0; i < len(pat); i++ {
		var v bool
		if horiz {
			v = m.modules[r][c+i]
		} else {
			v = m.modules[r+i][c]
		}
		if v != pat[i] {
			return false
		}
	}
	return true
}

var errEmpty = errors.New("qrcode: empty input")

// SVG 把矩陣算成一張自成一體的 SVG 字串(無外部相依、可直接塞進 <img> 的
// data URI 或 innerHTML)。modulesize 是每個模組的像素邊長,quiet 是靜區
// (四周留白)的模組數(規格建議至少 4)。深色模組畫成 rect,底色白。
func (m *Matrix) SVG(moduleSize, quiet int) string {
	if moduleSize <= 0 {
		moduleSize = 4
	}
	if quiet < 0 {
		quiet = 4
	}
	dim := (m.Size + 2*quiet) * moduleSize
	var b []byte
	b = append(b, []byte(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code">`,
		dim, dim, dim, dim))...)
	b = append(b, []byte(fmt.Sprintf(`<rect width="%d" height="%d" fill="#ffffff"/>`, dim, dim))...)
	b = append(b, []byte(`<path fill="#000000" d="`)...)
	for r := 0; r < m.Size; r++ {
		for c := 0; c < m.Size; c++ {
			if m.modules[r][c] {
				x := (c + quiet) * moduleSize
				y := (r + quiet) * moduleSize
				b = append(b, []byte(fmt.Sprintf("M%d %dh%dv%dh-%dz", x, y, moduleSize, moduleSize, moduleSize))...)
			}
		}
	}
	b = append(b, []byte(`"/></svg>`)...)
	return string(b)
}

// EncodeSVG 是給呼叫端最方便的入口:text -> QR -> SVG 字串。
func EncodeSVG(text string, moduleSize, quiet int) (string, error) {
	if text == "" {
		return "", errEmpty
	}
	m, err := Encode(text)
	if err != nil {
		return "", err
	}
	return m.SVG(moduleSize, quiet), nil
}
