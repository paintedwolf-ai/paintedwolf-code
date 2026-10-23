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
        } else if (command != 's' || fd < 0 || fcntl(fd,F_GETFD) < 0) {
            if (fd >= 0) close(fd);
            return 6;
        }
    }
    if (fd >= 0) close(fd);
    return 6;
}

int main(int argc, char **argv) {
    if (argc == 3 && strcmp(argv[1],"--engine-read") == 0) return read_volume(argv[2],1) ? 0 : 14;
    if (argc != 3) return 2;
    const char *payload = argv[2];
    int hold = strcmp(argv[1],"--protected-hold") == 0;
    int protected = hold || strcmp(argv[1],"--protected-read") == 0;
    if (!protected && strcmp(argv[1],"--ordinary-read") != 0) return 2;
    gtk_init(NULL,NULL);
    GtkWidget *chooser = gtk_file_chooser_dialog_new("Volume fixture",NULL,GTK_FILE_CHOOSER_ACTION_OPEN,"Open",GTK_RESPONSE_ACCEPT,NULL);
    gtk_widget_show_all(chooser);
    gtk_file_chooser_set_filename(GTK_FILE_CHOOSER(chooser),payload);
    gint64 deadline = g_get_monotonic_time()+2*G_TIME_SPAN_SECOND;
    gchar *selected = NULL;
    while (g_get_monotonic_time() < deadline) {
        while (g_main_context_iteration(NULL,FALSE)) {}
        g_free(selected);
        selected = gtk_file_chooser_get_filename(GTK_FILE_CHOOSER(chooser));
        if (selected && strcmp(selected,payload) == 0 && gtk_widget_get_mapped(chooser)) break;
        g_usleep(1000);
    }
    if (!selected || strcmp(selected,payload) != 0 || !gtk_widget_get_mapped(chooser) || !read_volume(selected,protected)) return 3;
    g_free(selected);
    if (protected) {
        char executable[4096];
        ssize_t length = readlink("/proc/self/exe",executable,sizeof(executable)-1);
        if (length < 1 || (size_t)length >= sizeof(executable)-1) return 4;
        executable[length] = 0;
        char *slash = strrchr(executable,'/');
        if (!slash || (size_t)(slash-executable)+4 >= sizeof(executable)) return 4;
        strcpy(slash+1,"pw");
        gchar *arguments[] = {executable,"--engine-read",(gchar *)payload,NULL};
        gint status = 0;
        if (!g_spawn_sync(NULL,arguments,NULL,0,NULL,NULL,NULL,NULL,&status,NULL) || !g_spawn_check_wait_status(status,NULL)) return 5;
    }
    printf("PWGTK01 {\"gtk_initialized\":true,\"chooser_read\":true,\"engine_read\":%s}\n",protected ? "true" : "false");
    fflush(stdout);
    if (hold) {
        int result = hold_volume(payload);
        gtk_widget_destroy(chooser);
        return result;
    }
    gtk_widget_destroy(chooser);
    return 0;
}
