/**
 * Copies text to the clipboard, on plain HTTP as well as on HTTPS.
 *
 * `navigator.clipboard` is declared `[SecureContext]` for the same reason as
 * `crypto.randomUUID` in `lib/uuid`, and is `undefined` when Console is
 * opened at `http://<host>:8080`. Copy controls sit on identifiers an operator
 * is about to paste into a terminal — a Cluster id, an Enrollment Token, a
 * command — so losing them on an insecure origin is losing the point of showing
 * the value at all.
 *
 * Rejects when the text did not reach the clipboard, so a caller keeps
 * reporting the failure instead of showing a checkmark that lies.
 */
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch (error) {
      // A secure origin can still refuse: the document is not focused, or the
      // user denied the permission. The selection path needs neither, so try it
      // before giving up, and report the original reason if it fails as well.
      if (!copyBySelection(text)) {
        throw error;
      }
      return;
    }
  }
  if (!copyBySelection(text)) {
    throw new Error("浏览器不允许写入剪贴板");
  }
}

/**
 * The pre-Clipboard-API path: select the text, let the browser copy the
 * selection. `document.execCommand` is deprecated and still implemented
 * everywhere, and on an insecure origin it is the only path there is.
 */
function copyBySelection(text: string): boolean {
  const field = document.createElement("textarea");
  field.value = text;
  field.setAttribute("readonly", "");
  // Off-screen rather than hidden: an element that is not rendered cannot hold
  // a selection, and the selection is what gets copied. Fixed positioning keeps
  // the page from scrolling to it.
  field.style.position = "fixed";
  field.style.top = "0";
  field.style.left = "-9999px";
  document.body.append(field);

  const previous = document.activeElement;
  field.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    field.remove();
    // The copy control that was clicked keeps its focus ring, so keyboard
    // navigation resumes where it was rather than at the top of the document.
    if (previous instanceof HTMLElement) {
      previous.focus();
    }
  }
}
