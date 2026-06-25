import { CloseCircleFilled, SearchOutlined } from "@ant-design/icons";
import { Button, Input } from "antd";
import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useT } from "../i18n";

export default function SearchHeaderControls() {
  const nav = useNavigate();
  const t = useT();
  const [searchParams] = useSearchParams();
  const qParam = searchParams.get("q")?.trim() ?? "";
  const [keyword, setKeyword] = useState(qParam);

  useEffect(() => {
    setKeyword(qParam);
  }, [qParam]);

  const doSearch = () => {
    const v = keyword.trim();
    nav(v ? `/search?q=${encodeURIComponent(v)}` : "/search");
  };

  const clearSearch = () => {
    setKeyword("");
    nav("/search");
  };

  return (
    <div className="app-header-search-controls">
      <Input
        className="app-header-search-input"
        value={keyword}
        onChange={(e) => setKeyword(e.target.value)}
        onPressEnter={doSearch}
        prefix={<SearchOutlined style={{ color: "#666" }} />}
        suffix={
          keyword ? (
            <CloseCircleFilled
              role="button"
              aria-label={t("pages.search.clear_btn")}
              className="app-header-search-clear"
              onClick={clearSearch}
            />
          ) : null
        }
        placeholder={t("pages.search.keyword_placeholder")}
      />
      <Button type="primary" icon={<SearchOutlined />} onClick={doSearch}>
        {t("pages.search.search_btn")}
      </Button>
    </div>
  );
}
