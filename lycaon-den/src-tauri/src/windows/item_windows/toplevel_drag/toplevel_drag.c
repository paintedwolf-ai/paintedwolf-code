/*
 * Moves a torn-off file tab's window with the pointer during a GTK drag, using
 * xdg_toplevel_drag_v1.
 *
 * GTK3 keeps each drag's wl_data_source and each window's xdg_toplevel
 * private, so the shell executable defines wl_proxy_marshal_flags. It never
 * decodes or changes a request: every argument slot goes unchanged to
 * libwayland's own wl_proxy_marshal_flags. Wayland request arguments are all
 * integer class (wl_fixed_t is an int32), and on the supported ABIs (x86_64
 * System V, AArch64 AAPCS64) each variadic one occupies one 8-byte register or
 * stack slot. So the same slots forwarded in order reach libwayland exactly as
 * the caller passed them. A few unused trailing slots are read and passed
 * along; libwayland reads only the arguments the request's signature names.
 *
 * Two requests are observed, through the public wl_proxy_get_class, and only
 * while the shell armed them on the GTK main thread:
 *
 * - wl_data_device.start_drag: the toplevel drag is created for GTK's source
 *   first, as the protocol requires.
 * - The first request GTK sends on a new xdg_toplevel (set_title, before the
 *   window's first commit): that toplevel is attached before it maps.
 *
 * If libwayland's entry point cannot be found, or the ABI is not one of the
 * above, drags use ordinary compositor placement.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdarg.h>
#include <stdint.h>
#include <string.h>
#include <wayland-client.h>

#if (defined(__x86_64__) || defined(__aarch64__)) && defined(__linux__)
#define PW_FORWARDING_ABI 1
#else
#define PW_FORWARDING_ABI 0
#endif

/* Above the largest Wayland request's argument count we observe from GTK. */
#define SLOTS 16

typedef struct wl_proxy *(*marshal_flags_fn)(struct wl_proxy *, uint32_t, const struct wl_interface *,
                                             uint32_t, uint32_t, ...);

extern const struct wl_interface xdg_toplevel_drag_v1_interface;

/* Object arguments need only the proxy; a new_id's interface is passed when marshalling. */
static const struct wl_interface *no_types[] = {NULL, NULL, NULL};

static const struct wl_message drag_requests[] = {
    {"destroy", "", no_types},
    {"attach", "oii", no_types},
};

const struct wl_interface xdg_toplevel_drag_v1_interface = {
    "xdg_toplevel_drag_v1", 1, 2, drag_requests, 0, NULL,
};

static const struct wl_interface *get_drag_types[] = {
    &xdg_toplevel_drag_v1_interface,
    &wl_data_source_interface,
};

static const struct wl_message manager_requests[] = {
    {"destroy", "", no_types},
    {"get_xdg_toplevel_drag", "no", get_drag_types},
};

const struct wl_interface xdg_toplevel_drag_manager_v1_interface = {
    "xdg_toplevel_drag_manager_v1", 1, 2, manager_requests, 0, NULL,
};

static struct {
    marshal_flags_fn real;
    int resolved;
    int disabled;
    struct wl_display *display;
    struct wl_proxy *manager;
    int arm_source;
    int arm_toplevel;
    int32_t x_offset;
    int32_t y_offset;
    struct wl_proxy *drag;
    int attached;
} state;

/* Swapped by tests to exercise a libwayland without the entry point. */
static void *(*resolve)(void *, const char *) = dlsym;

static marshal_flags_fn real_marshal(void)
{
    if (!state.resolved) {
        state.resolved = 1;
        if (PW_FORWARDING_ABI)
            state.real = (marshal_flags_fn)resolve(RTLD_NEXT, "wl_proxy_marshal_flags");
        if (!state.real)
            state.disabled = 1;
    }
    return state.real;
}

static int is_class(struct wl_proxy *proxy, const char *name)
{
    const char *class = proxy ? wl_proxy_get_class(proxy) : NULL;
    return class && strcmp(class, name) == 0;
}

static void create_drag(struct wl_proxy *source)
{
    union wl_argument args[2];
    args[0].n = 0;
    args[1].o = (struct wl_object *)source;
    state.drag = wl_proxy_marshal_array_flags(state.manager, 1, &xdg_toplevel_drag_v1_interface,
                                              wl_proxy_get_version(state.manager), 0, args);
}

static void attach(struct wl_proxy *toplevel)
{
    union wl_argument args[3];
    args[0].o = (struct wl_object *)toplevel;
    args[1].i = state.x_offset;
    args[2].i = state.y_offset;
    wl_proxy_marshal_array_flags(state.drag, 1, NULL, wl_proxy_get_version(state.drag), 0, args);
    state.attached = 1;
}

static void observe(struct wl_proxy *proxy, uint32_t opcode, uintptr_t first)
{
    if (state.arm_source && opcode == 0 && is_class(proxy, "wl_data_device") && first) {
        state.arm_source = 0;
        create_drag((struct wl_proxy *)first);
    } else if (state.arm_toplevel && state.drag && is_class(proxy, "xdg_toplevel")) {
        state.arm_toplevel = 0;
        attach(proxy);
    }
}

struct wl_proxy *wl_proxy_marshal_flags(struct wl_proxy *proxy, uint32_t opcode,
                                        const struct wl_interface *interface, uint32_t version,
                                        uint32_t flags, ...)
{
    uintptr_t slot[SLOTS];
    va_list ap;
    va_start(ap, flags);
    for (int i = 0; i < SLOTS; i++)
        slot[i] = va_arg(ap, uintptr_t);
    va_end(ap);
    marshal_flags_fn real = real_marshal();
    if (!real)
        return NULL;
    if (!state.disabled)
        observe(proxy, opcode, slot[0]);
    return real(proxy, opcode, interface, version, flags, slot[0], slot[1], slot[2], slot[3], slot[4],
                slot[5], slot[6], slot[7], slot[8], slot[9], slot[10], slot[11], slot[12], slot[13],
                slot[14], slot[15]);
}

static void global(void *data, struct wl_registry *registry, uint32_t name, const char *interface,
                   uint32_t version)
{
    (void)data;
    (void)version;
    if (strcmp(interface, "xdg_toplevel_drag_manager_v1") == 0 && !state.manager)
        state.manager = wl_registry_bind(registry, name, &xdg_toplevel_drag_manager_v1_interface, 1);
}

static void global_remove(void *data, struct wl_registry *registry, uint32_t name)
{
    (void)data;
    (void)registry;
    (void)name;
}

static const struct wl_registry_listener registry_listener = {global, global_remove};

/* Returns 1 when forwarding is available and the compositor offers the protocol. */
int pw_toplevel_drag_init(struct wl_display *display)
{
    if (state.display)
        return state.manager != NULL;
    state.display = display;
    if (!real_marshal())
        return 0;
    struct wl_event_queue *queue = wl_display_create_queue(display);
    if (!queue)
        return 0;
    struct wl_display *wrapper = wl_proxy_create_wrapper(display);
    if (!wrapper) {
        wl_event_queue_destroy(queue);
        return 0;
    }
    wl_proxy_set_queue((struct wl_proxy *)wrapper, queue);
    struct wl_registry *registry = wl_display_get_registry(wrapper);
    wl_proxy_wrapper_destroy(wrapper);
    if (registry) {
        wl_registry_add_listener(registry, &registry_listener, NULL);
        wl_display_roundtrip_queue(display, queue);
        wl_registry_destroy(registry);
    }
    if (state.manager)
        wl_proxy_set_queue(state.manager, NULL);
    wl_event_queue_destroy(queue);
    return state.manager != NULL;
}

/* The next start_drag gets a toplevel drag. */
void pw_toplevel_drag_arm_source(void)
{
    if (state.manager && !state.disabled && !state.drag)
        state.arm_source = 1;
}

/* The next xdg_toplevel GTK configures is attached with this offset from the pointer. */
void pw_toplevel_drag_arm_toplevel(int32_t x_offset, int32_t y_offset)
{
    if (state.drag && !state.attached) {
        state.arm_toplevel = 1;
        state.x_offset = x_offset;
        state.y_offset = y_offset;
    }
}

/* Nothing else is attached once the window the shell showed was configured. */
void pw_toplevel_drag_disarm_toplevel(void)
{
    state.arm_toplevel = 0;
}

int pw_toplevel_drag_active(void)
{
    return state.drag != NULL;
}

int pw_toplevel_drag_attached(void)
{
    return state.attached;
}

/* After the drag ended (dropped or cancelled), release the drag object. */
void pw_toplevel_drag_finish(void)
{
    state.arm_source = 0;
    state.arm_toplevel = 0;
    if (state.drag)
        wl_proxy_marshal_array_flags(state.drag, 0, NULL, wl_proxy_get_version(state.drag),
                                     WL_MARSHAL_FLAG_DESTROY, NULL);
    state.drag = NULL;
    state.attached = 0;
}

#ifdef PW_TOPLEVEL_DRAG_TESTS
static void *missing(void *handle, const char *name)
{
    (void)handle;
    (void)name;
    return NULL;
}

/* Starts over as a process whose libwayland lacks the entry point. */
void pw_toplevel_drag_test_without_libwayland_entry(void)
{
    memset(&state, 0, sizeof state);
    resolve = missing;
}

int pw_toplevel_drag_test_disabled(void)
{
    return state.disabled;
}
#endif
