"""PTY assertion screen with the scrolling sequences used by the renderer."""
import pyte


class Screen(pyte.Screen):
    # pyte 0.8.2 omits CSI S/T. The terminal renderer uses these to move an
    # existing diff inside its scrolling margins instead of repainting it.
    # https://invisible-island.net/xterm/ctlseqs/ctlseqs.html
    def scroll_up(self, count=None):
        top, bottom = self.margins or (0, self.lines - 1)
        position = self.cursor.y
        self.cursor.y = bottom
        for _ in range(min(count or 1, bottom - top + 1)):
            self.index()
        self.cursor.y = position

    def scroll_down(self, count=None):
        top, bottom = self.margins or (0, self.lines - 1)
        position = self.cursor.y
        self.cursor.y = top
        for _ in range(min(count or 1, bottom - top + 1)):
            self.reverse_index()
        self.cursor.y = position


class Stream(pyte.Stream):
    csi = dict(pyte.Stream.csi, S="scroll_up", T="scroll_down")
    events = pyte.Stream.events | {"scroll_up", "scroll_down"}
