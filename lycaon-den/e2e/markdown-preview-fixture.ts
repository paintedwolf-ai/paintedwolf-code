export const MARKDOWN_PREVIEW_TITLE = "Preview scroll fixture";

export function largeMarkdownPreviewFixture(): string {
  return `# ${MARKDOWN_PREVIEW_TITLE}\n\n` + Array.from({ length: 3000 }, (_, index) =>
    `## Package ${index}\n\n${"Paragraph text for preview scrolling. ".repeat(12)}\n\n\`\`\`text\n${"Licensed example code\n".repeat(8 + index % 16)}\`\`\`\n\n`,
  ).join("") + "End of preview fixture.\n";
}
