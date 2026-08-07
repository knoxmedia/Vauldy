import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { MediaItem } from "../api/client";
import MediaPosterImg from "./MediaPosterImg";

function media(overrides: Partial<MediaItem> = {}): MediaItem {
  return {
    id: 42,
    library_id: 1,
    file_id: "file-42",
    title: "Poster item",
    file_path: "poster.mkv",
    file_type: "video",
    duration: 0,
    width: 0,
    height: 0,
    format: "",
    status: "active",
    poster_url: "https://cdn.example/old.jpg",
    ingest_generation: 1,
    ...overrides,
  };
}

describe("MediaPosterImg", () => {
  it("restores hidden image when poster source changes", () => {
    const onFinalError = vi.fn();
    const { container, rerender } = render(
      <MediaPosterImg item={media()} onFinalError={onFinalError} />,
    );
    const img = container.querySelector("img")!;

    fireEvent.error(img);
    expect(img).toHaveAttribute("src", "/uploads/posters/42.jpg");
    fireEvent.error(img);
    expect(img).toHaveStyle({ display: "none" });
    expect(onFinalError).toHaveBeenCalledTimes(1);

    rerender(
      <MediaPosterImg
        item={media({ poster_url: "https://cdn.example/new.jpg", ingest_generation: 2 })}
        onFinalError={onFinalError}
      />,
    );

    expect(img).toHaveAttribute("src", "https://cdn.example/new.jpg");
    expect(img.style.display).toBe("");
    fireEvent.error(img);
    expect(img).toHaveAttribute("src", "/uploads/posters/42.jpg");
    expect(onFinalError).toHaveBeenCalledTimes(1);
  });
});
