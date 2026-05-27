package photogeocode

import "testing"

func TestPlaceFromNominatim(t *testing.T) {
	p := placeFromNominatim(nominatimResp{
		DisplayName: "成都市, 四川省, 中国",
		Address: map[string]string{
			"city":    "成都市",
			"state":   "四川省",
			"country": "中国",
		},
	})
	if p.LocationName != "四川省成都市" {
		t.Fatalf("name=%q", p.LocationName)
	}
	if p.PlaceID != "place:四川省成都市_四川省" {
		t.Fatalf("place_id=%q", p.PlaceID)
	}
}

func TestBuildPlaceIDPrefecture(t *testing.T) {
	p := placeFromNominatim(nominatimResp{
		Address: map[string]string{
			"state":   "阿坝藏族羌族自治州",
			"country": "中国",
		},
	})
	if p.LocationName != "阿坝藏族羌族自治州" {
		t.Fatalf("name=%q", p.LocationName)
	}
}

func TestPlaceFromBigDataCloudBeijing(t *testing.T) {
	p := placeFromBigDataCloud(bigDataCloudResp{
		CountryCode:          "CN",
		CountryName:          "中华人民共和国",
		PrincipalSubdivision: "北京市",
		City:                 "北京市",
		Locality:             "朝陽區",
	})
	if p.LocationName != "北京市朝阳区" {
		t.Fatalf("name=%q", p.LocationName)
	}
}

func TestPlaceFromBigDataCloudShanghai(t *testing.T) {
	p := placeFromBigDataCloud(bigDataCloudResp{
		CountryCode:          "CN",
		CountryName:          "中华人民共和国",
		PrincipalSubdivision: "上海市",
		City:                 "上海市",
		Locality:             "静安区",
	})
	if p.LocationName != "上海市静安区" {
		t.Fatalf("name=%q", p.LocationName)
	}
}

func TestFormatChinaPlaceName(t *testing.T) {
	if got := formatChinaPlaceName("北京市", "北京市", "大兴区"); got != "北京市大兴区" {
		t.Fatalf("got=%q", got)
	}
	if got := formatChinaPlaceName("广东省", "深圳市", "南山区"); got != "广东省深圳市南山区" {
		t.Fatalf("got=%q", got)
	}
}

func TestCacheKey(t *testing.T) {
	if cacheKey(39.904211, 116.407395) != "39.904,116.407" {
		t.Fatal("unexpected cache key")
	}
}

func TestLooksLikeCoordLabel(t *testing.T) {
	if !looksLikeCoordLabel("39.6584°, 116.2368°") {
		t.Fatal("expected coord label")
	}
	if looksLikeCoordLabel("北京市朝阳区") {
		t.Fatal("unexpected coord label")
	}
}
