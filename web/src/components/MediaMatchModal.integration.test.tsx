// @vitest-environment jsdom
import React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { manualMatchMediaMock, parseScrapeTitleMock, searchScrapeMatchesMock } =
  vi.hoisted(() => ({
    manualMatchMediaMock: vi.fn(),
    parseScrapeTitleMock: vi.fn(),
    searchScrapeMatchesMock: vi.fn(),
  }));

vi.mock("../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/client")>();
  return {
    ...actual,
    manualMatchMedia: manualMatchMediaMock,
    parseScrapeTitle: parseScrapeTitleMock,
    searchScrapeMatches: searchScrapeMatchesMock,
  };
});
vi.mock("../i18n", () => ({ useT: () => (key: string) => key }));
vi.mock("@ant-design/icons", () => ({ DownOutlined: () => null }));
vi.mock("antd", () => {
  const Button = ({
    children,
    onClick,
    disabled,
  }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button onClick={onClick} disabled={disabled}>
      {children}
    </button>
  );
  const Empty = Object.assign(
    ({ description }: { description?: React.ReactNode }) => (
      <div>{description}</div>
    ),
    { PRESENTED_IMAGE_SIMPLE: null },
  );
  const Input = Object.assign(
    ({ value, onChange }: React.InputHTMLAttributes<HTMLInputElement>) => (
      <input value={value} onChange={onChange} />
    ),
    { TextArea: () => null },
  );
  const Text = ({ children }: React.PropsWithChildren) => (
    <span>{children}</span>
  );
  return {
    Button,
    Empty,
    Input,
    InputNumber: () => null,
    Modal: ({ children, open }: React.PropsWithChildren<{ open?: boolean }>) =>
      open ? <div>{children}</div> : null,
    Select: () => null,
    Spin: () => <div>loading</div>,
    Typography: { Text, Paragraph: Text },
    message: { success: vi.fn(), error: vi.fn() },
  };
});

import MediaMatchModal from "./MediaMatchModal";

describe("MediaMatchModal series matching", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    parseScrapeTitleMock.mockResolvedValue({
      title: "Series Query",
      year: 2023,
    });
    searchScrapeMatchesMock.mockResolvedValue({
      items: [
        {
          source: "tmdb",
          external_id: "movie-1",
          media_type: "movie",
          title: "Movie Result",
        },
        {
          source: "tmdb",
          external_id: "tv-1",
          media_type: "tv",
          title: "TV Result",
        },
      ],
    });
    manualMatchMediaMock.mockResolvedValue({ ok: true });
  });

  it("hides movie candidates and submits the TV candidate for the representative episode", async () => {
    render(
      <MediaMatchModal
        media={{ id: 108, title: "Series Query", year: 2023, file_path: "" }}
        matchKind="series"
        open
        onClose={() => {}}
      />,
    );

    await waitFor(() =>
      expect(searchScrapeMatchesMock).toHaveBeenCalledWith(
        expect.objectContaining({ query: "Series Query", year: 2023 }),
      ),
    );
    expect(screen.queryByText("Movie Result")).toBeNull();
    fireEvent.click(await screen.findByText("TV Result"));
    await waitFor(() =>
      expect(manualMatchMediaMock).toHaveBeenCalledWith(
        108,
        expect.objectContaining({
          external_id: "tv-1",
          media_type: "tv",
          query: "Series Query",
          year: 2023,
        }),
      ),
    );
  });
});
