#define _GNU_SOURCE
#include <gtk/gtk.h>
#include <fcntl.h>
#include <stdio.h>
#include <sys/stat.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc != 2) return 2;
    const char *report = argv[1];
    struct stat input;
    if (fstat(0, &input) != 0) return 3;
    if (!gtk_init_check(&argc, &argv)) return 4;
    GtkWidget *chooser = gtk_file_chooser_dialog_new("Stream fixture", NULL,
        GTK_FILE_CHOOSER_ACTION_OPEN, "Close", GTK_RESPONSE_CANCEL, NULL);
    gtk_widget_show_all(chooser);
    for (int attempt = 0; attempt < 100; attempt++) {
        while (gtk_events_pending()) gtk_main_iteration();
        if (gtk_widget_get_mapped(chooser)) break;
        g_usleep(1000);
    }
    if (!gtk_widget_get_mapped(chooser)) return 5;
    int fd = open(report, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
    if (fd < 0) return 6;
    if (dprintf(fd, "{\"gtk_initialized\":true,\"chooser_mapped\":true,\"stdin_mode\":%u,\"stdin_inode\":%llu}\n",
                input.st_mode, (unsigned long long)input.st_ino) < 0) return 7;
    if (close(fd) != 0) return 8;
    gtk_widget_destroy(chooser);
    return 0;
}
