import { EditorSelection } from "@codemirror/state";
import { layer, RectangleMarker, type EditorView } from "@codemirror/view";
import { windowColorStyle, type WindowColor } from "../../contributions/window-color-palette.ts";
import { selectionOutline } from "../tree/selection-outline.ts";

type WindowSelection = { window: { clientId: string }; anchor: number; head: number; color: WindowColor | undefined };

class WindowSelectionMarker extends RectangleMarker {
  constructor(rectangle: RectangleMarker, readonly selection: WindowSelection, readonly outline?: string) {
    super(outline ? "cm-document-selection-outline" : "cm-document-selection", rectangle.left, rectangle.top, rectangle.width, rectangle.height);
  }

  eq(other: RectangleMarker): boolean {
    return other instanceof WindowSelectionMarker && super.eq(other)
      && this.outline === other.outline
      && this.selection.window.clientId === other.selection.window.clientId
      && this.selection.color?.selection === other.selection.color?.selection
      && this.selection.color?.caret === other.selection.color?.caret;
  }

  private paint(element: HTMLElement): void {
    element.dataset.windowClient = this.selection.window.clientId;
    for (const [property, value] of Object.entries(windowColorStyle(this.selection.color))) {
      element.style.setProperty(property, value);
    }
  }

  draw(): HTMLDivElement {
    const element = super.draw();
    this.paint(element);
    if (this.outline) {
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("width", "100%");
      svg.setAttribute("height", "100%");
      const path = document.createElementNS(svg.namespaceURI, "path");
      path.setAttribute("d", this.outline);
      svg.append(path);
      element.append(svg);
    }
    return element;
  }

  update(element: HTMLElement, previous: RectangleMarker): boolean {
    if (!(previous instanceof WindowSelectionMarker) || !super.update(element, previous)) return false;
    this.paint(element);
    if (this.outline) element.querySelector("path")?.setAttribute("d", this.outline);
    return true;
  }
}

function markers(view: EditorView, selection: WindowSelection): WindowSelectionMarker[] {
  if (selection.anchor === selection.head) return [];
  const rectangles = RectangleMarker.forRange(view, "cm-document-selection", EditorSelection.range(selection.anchor, selection.head));
  if (!rectangles.length) return [];
  const left = Math.min(...rectangles.map(rect => rect.left)), top = Math.min(...rectangles.map(rect => rect.top));
  const right = Math.max(...rectangles.map(rect => rect.left + (rect.width ?? 0)));
  const bottom = Math.max(...rectangles.map(rect => rect.top + rect.height));
  const outline = selectionOutline(rectangles).map(edge =>
    `M${edge.x1 - left} ${edge.y1 - top}L${edge.x2 - left} ${edge.y2 - top}`).join("");
  const result = rectangles.map(rect => new WindowSelectionMarker(rect, selection));
  if (outline) result.push(new WindowSelectionMarker(new RectangleMarker("", left, top, right - left, bottom - top), selection, outline));
  return result;
}

/** Use the editor's selection geometry for wrapping, empty lines, and bidirectional text. */
export function documentSelectionLayer(selections: (view: EditorView) => readonly WindowSelection[]) {
  return layer({
    above: false,
    class: "cm-document-selection-layer",
    update: update => update.docChanged || update.viewportChanged || update.transactions.length > 0,
    markers: view => selections(view).flatMap(selection => markers(view, selection)),
  });
}
