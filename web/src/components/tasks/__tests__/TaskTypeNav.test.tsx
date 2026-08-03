import { describe, it, expect, vi } from "vitest";
import { render, screen, within, fireEvent } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { TaskTypeNav } from "../TaskTypeNav";
import type { Registry, TaskGroup } from "../../../api/taskControl";

const overviewGroup: TaskGroup = {
  label: "tasks.group.overview",
  selectable: false,
  types: [],
};

const videoGroup: TaskGroup = {
  label: "tasks.group.video",
  selectable: false,
  types: [
    {
      type: "transcode",
      group: "tasks.group.video",
      route: "tasks/video/transcode",
      family: "video_post_processing",
      source_mappings: [{ kind: "orchestration" }],
      columns: [],
      filters: [],
      capabilities: [],
      available: true,
    },
    {
      type: "optimize",
      group: "tasks.group.video",
      route: "tasks/video/optimize",
      family: "video_post_processing",
      source_mappings: [{ kind: "orchestration" }],
      columns: [],
      filters: [],
      capabilities: [],
      available: true,
    },
  ],
};

const imageGroup: TaskGroup = {
  label: "tasks.group.image",
  selectable: false,
  types: [
    {
      type: "photo_classify",
      group: "tasks.group.image",
      route: "tasks/image/photo_classify",
      family: "image_processing",
      source_mappings: [{ kind: "orchestration" }],
      columns: [],
      filters: [],
      capabilities: [],
      available: true,
    },
    {
      type: "image_ocr",
      group: "tasks.group.image",
      route: "tasks/image/image_ocr",
      family: "image_processing",
      source_mappings: [],
      columns: [],
      filters: [],
      capabilities: [],
      available: false,
    },
  ],
};

function makeRegistry(groups: TaskGroup[]): Registry {
  return { groups };
}

describe("TaskTypeNav", () => {
  it("renders Overview as first tab and is selectable", () => {
    const reg = makeRegistry([overviewGroup]);
    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="overview" onSelect={() => {}} />
      </MemoryRouter>,
    );

    const tabs = screen.getByRole("tablist");
    const overviewTab = within(tabs).getByRole("tab", { name: /overview/i });
    expect(overviewTab).toBeInTheDocument();
    expect(overviewTab).toHaveAttribute("aria-selected", "true");
  });

  it("renders group labels are not tab elements", () => {
    const reg = makeRegistry([overviewGroup, videoGroup]);
    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="overview" onSelect={() => {}} />
      </MemoryRouter>,
    );

    const tabs = screen.getByRole("tablist");
    const allTabs = within(tabs).getAllByRole("tab");
    // "overview", "transcode" (-> "Transcode"), "optimize" (-> "Optimize")
    const tabLabels = allTabs.map((t) => t.textContent);
    // Group label should not be among tab elements
    expect(tabLabels).not.toContain("tasks.group.video");
  });

  it("every type has its own tab", () => {
    const reg = makeRegistry([overviewGroup, videoGroup, imageGroup]);
    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="overview" onSelect={() => {}} />
      </MemoryRouter>,
    );

    const tabs = screen.getByRole("tablist");
    // overview + transcode + optimize + photo_classify + image_ocr = 5 tabs
    const allTabs = within(tabs).getAllByRole("tab");
    expect(allTabs.length).toBe(5);

    // Available types should render their display names
    expect(screen.getByRole("tab", { name: /transcode/i })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /optimize/i })).toBeInTheDocument();
    // photo_classify -> "Photo Classify"
    expect(screen.getByRole("tab", { name: /photo classify/i })).toBeInTheDocument();
    // image_ocr -> "Image Ocr" (unavailable, still renders)
    expect(screen.getByRole("tab", { name: /image ocr/i })).toBeInTheDocument();
  });

  it("keyboard End navigates to last tab", () => {
    const reg = makeRegistry([overviewGroup, videoGroup]);
    const onSelect = vi.fn();

    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="overview" onSelect={onSelect} />
      </MemoryRouter>,
    );

    const tabs = screen.getByRole("tablist");
    fireEvent.keyDown(tabs, { key: "End" });

    const lastType = videoGroup.types![videoGroup.types!.length - 1]!;
    expect(onSelect).toHaveBeenCalledWith(lastType.type);
  });

  it("keyboard Home navigates to first tab", () => {
    const reg = makeRegistry([overviewGroup, videoGroup]);
    const onSelect = vi.fn();

    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="transcode" onSelect={onSelect} />
      </MemoryRouter>,
    );

    const tabs = screen.getByRole("tablist");
    fireEvent.keyDown(tabs, { key: "Home" });

    expect(onSelect).toHaveBeenCalledWith("overview");
  });

  it("keyboard Left navigates to previous tab", () => {
    const reg = makeRegistry([overviewGroup, videoGroup]);
    const onSelect = vi.fn();

    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="optimize" onSelect={onSelect} />
      </MemoryRouter>,
    );

    const tabs = screen.getByRole("tablist");
    fireEvent.keyDown(tabs, { key: "ArrowLeft" });

    expect(onSelect).toHaveBeenCalledWith("transcode");
  });

  it("keyboard Right navigates to next tab", () => {
    const reg = makeRegistry([overviewGroup, videoGroup]);
    const onSelect = vi.fn();

    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="overview" onSelect={onSelect} />
      </MemoryRouter>,
    );

    const tabs = screen.getByRole("tablist");
    fireEvent.keyDown(tabs, { key: "ArrowRight" });

    expect(onSelect).toHaveBeenCalledWith("transcode");
  });

  it("tab panel is associated with active type via aria-controls", () => {
    const reg = makeRegistry([overviewGroup, videoGroup]);
    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="transcode" onSelect={() => {}} />
      </MemoryRouter>,
    );

    const tab = screen.getByRole("tab", { name: /transcode/i });
    expect(tab).toHaveAttribute("aria-selected", "true");
    expect(tab).toHaveAttribute("aria-controls", "task-panel-transcode");
  });

  it("renders empty state for empty registry", () => {
    const reg = makeRegistry([]);
    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="" onSelect={() => {}} />
      </MemoryRouter>,
    );

    // Should still render a tablist even if empty
    expect(screen.getByRole("tablist")).toBeInTheDocument();
  });

  it("triggers onSelect when a tab button is clicked", () => {
    const reg = makeRegistry([overviewGroup, videoGroup]);
    const onSelect = vi.fn();

    render(
      <MemoryRouter>
        <TaskTypeNav registry={reg} activeType="overview" onSelect={onSelect} />
      </MemoryRouter>,
    );

    const tab = screen.getByRole("tab", { name: /transcode/i });
    tab.click();
    expect(onSelect).toHaveBeenCalledWith("transcode");
  });
});
