#define _GNU_SOURCE
#include <gtk/gtk.h>
#include <sys/statvfs.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <poll.h>
#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static int read_volume(const char *path, int require_safe) {
    gchar *content = NULL;
    gsize size = 0;
    struct statvfs facts;
    if (!g_file_get_contents(path,&content,&size,NULL)) { fprintf(stderr,"volume read failed: %s\n",path); return 0; }
    int valid = size == strlen("caller-owned-volume\n") && memcmp(content,"caller-owned-volume\n",size) == 0;
    g_free(content);
    if (statvfs(path,&facts) != 0) { perror("volume statvfs"); return 0; }
    if (require_safe && (facts.f_flag&(ST_NOEXEC|ST_NOSUID|ST_NODEV)) != (ST_NOEXEC|ST_NOSUID|ST_NODEV)) return 0;
    return valid;
}

static int hold_command(void) {
    for (;;) {
        while (g_main_context_iteration(NULL,FALSE)) {}
        struct pollfd input = {STDIN_FILENO,POLLIN,0};
        int ready = poll(&input,1,10);
        if (ready < 0) {
            if (errno == EINTR) continue;
            return -2;
        }
        if (ready > 0) {
            unsigned char command;
            ssize_t count = read(STDIN_FILENO,&command,1);
            if (count == 1) return command;
            if (count == 0) return EOF;
            if (errno != EINTR) return -2;
        }
    }
}

static int hold_volume(const char *payload) {
    int fd = open(payload,O_RDONLY|O_CLOEXEC|O_NOFOLLOW);
    struct stat object;
    struct statvfs filesystem;
    if (fd < 0 || fstat(fd,&object) != 0 || !S_ISREG(object.st_mode) || fstatvfs(fd,&filesystem) != 0) return 6;
    if ((filesystem.f_flag&(ST_NOEXEC|ST_NOSUID|ST_NODEV)) != (ST_NOEXEC|ST_NOSUID|ST_NODEV)) {
        close(fd);
        return 6;
    }
    for (int commands = 0; commands < 64; commands++) {
        printf("PWGTKHOLD01 {\"held\":%s,\"pid\":%ld,\"fd\":%d,\"device\":%llu,\"inode\":%llu}\n",
            fd >= 0 ? "true" : "false",(long)getpid(),fd,(unsigned long long)object.st_dev,(unsigned long long)object.st_ino);
        fflush(stdout);
        int command = hold_command();
        if (command == EOF || command == 'q') {
            if (fd >= 0) close(fd);
            return 0;
        }
        if (command == 'c' && fd >= 0) {
            if (close(fd) != 0) return 6;
            fd = -1;
        } else if (command != 's' || (fd >= 0 && fcntl(fd,F_GETFD) < 0)) {
            if (fd >= 0) close(fd);
            return 6;
        }
    }
    if (fd >= 0) close(fd);
    return 6;
}

static int chooser_read(GtkWidget *chooser, const char *payload, int protected, const char *image) {
    gint64 deadline = g_get_monotonic_time()+2*G_TIME_SPAN_SECOND;
    gchar *selected = NULL;
    while (g_get_monotonic_time() < deadline) {
        if (g_file_test(payload,G_FILE_TEST_IS_REGULAR) && (!selected || strcmp(selected,payload) != 0))
            gtk_file_chooser_set_filename(GTK_FILE_CHOOSER(chooser),payload);
        while (g_main_context_iteration(NULL,FALSE)) {}
        g_free(selected);
        selected = gtk_file_chooser_get_filename(GTK_FILE_CHOOSER(chooser));
        if (selected && strcmp(selected,payload) == 0 && gtk_widget_get_mapped(chooser) && g_file_test(payload,G_FILE_TEST_IS_REGULAR)) break;
        g_usleep(1000);
    }
    int valid = selected && strcmp(selected,payload) == 0 && gtk_widget_get_mapped(chooser) && read_volume(selected,protected);
    g_free(selected);
    if (!valid) return 3;
    if (protected) {
        char executable[4096];
        // The protected entry writes argv[0] from its authenticated logical root.
        // The physical proc-exe alias can be hidden by the selected-root bind.
        if (image[0] != '/' || strlen(image) >= sizeof(executable)) return 4;
        strcpy(executable,image);
        char *slash = strrchr(executable,'/');
        if (!slash || (size_t)(slash-executable)+4 >= sizeof(executable)) return 4;
        strcpy(slash+1,"pw");
        gchar *arguments[] = {executable,"--engine-read",(gchar *)payload,NULL};
        gint status = 0;
        if (!g_spawn_sync(NULL,arguments,NULL,0,NULL,NULL,NULL,NULL,&status,NULL) || !g_spawn_check_wait_status(status,NULL)) return 5;
    }
    printf("PWGTK01 {\"gtk_initialized\":true,\"chooser_read\":true,\"engine_read\":%s}\n",protected ? "true" : "false");
    fflush(stdout);
    return 0;
}

// Start with no volume, then perform chooser reads only on explicit commands.
static int watch_volume(GtkWidget *chooser, const char *payload, const char *image) {
    gint64 deadline = g_get_monotonic_time()+2*G_TIME_SPAN_SECOND;
    while (!gtk_widget_get_mapped(chooser) && g_get_monotonic_time() < deadline) {
        while (g_main_context_iteration(NULL,FALSE)) {}
        g_usleep(1000);
    }
    if (!gtk_widget_get_mapped(chooser)) return 7;
    printf("PWGTKREADY01 {\"gtk_initialized\":true,\"pid\":%ld}\n",(long)getpid());
    fflush(stdout);
    for (int commands = 0; commands < 64; commands++) {
        int command = hold_command();
        if (command == EOF || command == 'q') return 0;
        if (command != 'r' && command != 'h') return 7;
        int result = chooser_read(chooser,payload,1,image);
        if (result != 0) return result;
        if (command == 'h') return hold_volume(payload);
    }
    return 7;
}

int main(int argc, char **argv) {
    if (argc == 3 && strcmp(argv[1],"--engine-read") == 0) return read_volume(argv[2],1) ? 0 : 14;
    if (argc != 3) return 2;
    const char *payload = argv[2];
    int hold = strcmp(argv[1],"--protected-hold") == 0;
    int watch = strcmp(argv[1],"--protected-watch") == 0;
    int protected = hold || watch || strcmp(argv[1],"--protected-read") == 0;
    if (!protected && strcmp(argv[1],"--ordinary-read") != 0) return 2;
    gtk_init(NULL,NULL);
    GtkWidget *chooser = gtk_file_chooser_dialog_new("Volume fixture",NULL,GTK_FILE_CHOOSER_ACTION_OPEN,"Open",GTK_RESPONSE_ACCEPT,NULL);
    gtk_widget_show_all(chooser);
    int result = watch ? watch_volume(chooser,payload,argv[0]) : chooser_read(chooser,payload,protected,argv[0]);
    if (!watch && result == 0 && hold) result = hold_volume(payload);
    gtk_widget_destroy(chooser);
    return result;
}
