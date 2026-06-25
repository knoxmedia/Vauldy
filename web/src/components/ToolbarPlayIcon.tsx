import type { SVGProps } from "react";

/** 播放三角（Plex toolbar play 560 路径，currentColor 填色） */
export default function ToolbarPlayIcon(props: SVGProps<SVGSVGElement>) {
  const { className, ...rest } = props;
  return (
    <svg
      viewBox="0 0 560 560"
      xmlns="http://www.w3.org/2000/svg"
      strokeMiterlimit={1.414}
      strokeLinejoin="round"
      className={className}
      aria-hidden
      {...rest}
    >
      <path fill="currentColor" d="m112 504l0-448 392 224-392 224" />
    </svg>
  );
}
