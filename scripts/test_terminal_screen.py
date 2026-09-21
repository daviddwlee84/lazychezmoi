import unittest

from terminal_screen import Screen, Stream


class TerminalScrollingTests(unittest.TestCase):
    def setUp(self):
        self.screen = Screen(5, 5)
        self.stream = Stream(self.screen)
        for row, value in enumerate("ABCDE", 1):
            self.stream.feed(f"\x1b[{row};1H{value * 5}")
        self.stream.feed("\x1b[2;4r\x1b[3;3H")

    def assert_rows_and_cursor(self, rows):
        self.assertEqual(self.screen.display, rows)
        self.assertEqual((self.screen.cursor.x, self.screen.cursor.y), (2, 2))

    def test_scroll_down_respects_margins_and_fragmented_input(self):
        for part in ("\x1b", "[", "2", "T"):
            self.stream.feed(part)
        self.assert_rows_and_cursor(["AAAAA", "     ", "     ", "BBBBB", "EEEEE"])

    def test_scroll_up_defaults_to_one_line(self):
        self.stream.feed("\x1b[S")
        self.assert_rows_and_cursor(["AAAAA", "CCCCC", "DDDDD", "     ", "EEEEE"])

    def test_scroll_down_zero_defaults_and_large_count_clears_only_region(self):
        self.stream.feed("\x1b[0T")
        self.assert_rows_and_cursor(["AAAAA", "     ", "BBBBB", "CCCCC", "EEEEE"])
        self.stream.feed("\x1b[999S")
        self.assert_rows_and_cursor(["AAAAA", "     ", "     ", "     ", "EEEEE"])


if __name__ == "__main__":
    unittest.main()
