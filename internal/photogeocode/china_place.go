package photogeocode

import "strings"

var cnTradToSimp = strings.NewReplacer(
	"區", "区", "縣", "县", "陽", "阳", "縣", "县",
	"東", "东", "興", "兴", "廣", "广", "滬", "沪",
	"臺", "台", "灣", "湾", "總", "总", "縮", "缩",
	"島", "岛", "門", "门", "長", "长", "馬", "马",
	"龍", "龙", "無", "无", "縣", "县", "鄉", "乡",
	"鎮", "镇", "裡", "里", "裏", "里", "陰", "阴",
	"陝", "陕", "遼", "辽", "閩", "闽", "貴", "贵",
	"雲", "云", "寧", "宁", "廠", "厂", "國", "国",
)

func simplifyCN(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return cnTradToSimp.Replace(s)
}

func isDirectMunicipality(name string) bool {
	switch simplifyCN(name) {
	case "北京市", "上海市", "天津市", "重庆市":
		return true
	default:
		return false
	}
}

// formatChinaPlaceName builds labels like 北京市朝阳区、上海市静安区.
func formatChinaPlaceName(province, city, district string) string {
	province = simplifyCN(province)
	city = simplifyCN(city)
	district = simplifyCN(district)

	if province == "" {
		province = city
	}
	if city == "" {
		city = province
	}

	if district != "" && (district == province || district == city) {
		district = ""
	}
	if district != "" && strings.HasPrefix(district, province) {
		return district
	}
	if district != "" && strings.HasPrefix(district, city) && city != province {
		return district
	}

	if isDirectMunicipality(province) || province == city {
		if district != "" {
			return province + district
		}
		return province
	}

	var parts []string
	if province != "" {
		parts = append(parts, province)
	}
	if city != "" && city != province {
		parts = append(parts, city)
	}
	if district != "" && district != city {
		parts = append(parts, district)
	}
	return strings.Join(parts, "")
}

func looksLikeCoordLabel(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "°") && strings.Contains(s, ",")
}
